package config

import (
	"fmt"
	"sort"
	"strings"
)

// YAMLSourceValue locates an existing mapping value in the original YAML subset
// accepted by LoadYAMLTreeBytes. Offsets are bytes, not re-encoded YAML positions.
// Tail contains the first line's whitespace/comment; Body retains block scalars.
type YAMLSourceValue struct {
	Start, End       int
	Head, Tail, Body string
	Indent           int
	Flow             bool
}

func (v YAMLSourceValue) WithValue(from YAMLSourceValue, newline string) (string, error) {
	if v.Flow && from.Body != "" {
		return "", fmt.Errorf("cannot preserve a block scalar inside a flow map; keep a block-style env mapping")
	}
	body := from.Body
	if body != "" {
		lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			indent := countIndent(line) + v.Indent - from.Indent
			if indent < 0 {
				return "", fmt.Errorf("invalid preserved scalar indentation")
			}
			lines[i] = strings.Repeat(" ", indent) + strings.TrimLeft(line, " ")
		}
		body = newline + strings.Join(lines, newline)
	}
	return from.Head + v.Tail + body, nil
}

type yamlSourceLine struct {
	start, end, indent int
	text               string
}

// YAMLSourceMap locates all members without serializing unrelated content.
// Ambiguous duplicate keys fail closed rather than leaving a credential visible.
func YAMLSourceMap(source string, path ...string) (map[string]YAMLSourceValue, error) {
	if _, err := LoadYAMLTreeBytes([]byte(source)); err != nil {
		return nil, err
	}
	lines := []yamlSourceLine{}
	offset := 0
	for _, raw := range strings.SplitAfter(source, "\n") {
		line := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		clean := stripInlineCommentWithDoubleQuoteEscapes(line, false)
		if offset == 0 {
			clean = strings.TrimPrefix(clean, "\ufeff")
		}
		if strings.TrimSpace(clean) != "" {
			lines = append(lines, yamlSourceLine{offset, offset + len(line), countIndent(clean), strings.TrimSpace(clean)})
		}
		offset += len(raw)
	}
	if len(lines) == 0 {
		return map[string]YAMLSourceValue{}, nil
	}
	var walk func(int, int, []string) (map[string]YAMLSourceValue, error)
	walk = func(lo, hi int, keys []string) (map[string]YAMLSourceValue, error) {
		result := map[string]YAMLSourceValue{}
		seen := map[string]bool{}
		selectedLo, selectedHi := -1, -1
		var selected YAMLSourceValue
		indent := lines[lo].indent
		for i := lo; i < hi; {
			line := lines[i]
			if line.indent != indent {
				return nil, fmt.Errorf("unsupported YAML mapping layout")
			}
			key, raw, has := splitYAMLKeyValue(line.text)
			if seen[key] {
				return nil, fmt.Errorf("duplicate YAML mapping key %q", key)
			}
			seen[key] = true
			j := i + 1
			for j < hi && (lines[j].indent > indent || (!has && lines[j].indent == indent && (strings.HasPrefix(lines[j].text, "- ") || lines[j].text == "-"))) {
				j++
			}
			original := source[line.start:line.end]
			clean := stripInlineCommentWithDoubleQuoteEscapes(original, false)
			tokenEnd := line.start + len(strings.TrimRight(clean, " \t"))
			start := tokenEnd - len(raw)
			end := line.end
			body := ""
			if isYAMLBlockScalar(raw) || (!has && j > i+1) {
				end = lines[j-1].end
				// Include indented comment lines in block scalars: they are data,
				// even though the legacy semantic parser drops comment-only lines.
				if isYAMLBlockScalar(raw) {
					pos := line.end
					for pos < len(source) && (source[pos] == '\r' || source[pos] == '\n') {
						pos++
					}
					for pos < len(source) {
						stop := strings.IndexByte(source[pos:], '\n')
						if stop < 0 {
							stop = len(source) - pos
						}
						physical := strings.TrimSuffix(source[pos:pos+stop], "\r")
						if strings.TrimSpace(physical) != "" {
							if countIndent(physical) <= indent {
								break
							}
							end = pos + len(physical)
						}
						pos += stop + 1
					}
				}
				bodyStart := strings.IndexByte(source[line.end:], '\n')
				if bodyStart >= 0 && line.end+bodyStart+1 <= end {
					body = source[line.end+bodyStart+1 : end]
				}
			}
			value := YAMLSourceValue{Start: start, End: end, Head: raw, Tail: source[tokenEnd:line.end], Body: body, Indent: indent}
			if len(keys) == 0 {
				result[key] = value
			} else if key == keys[0] {
				selectedLo, selectedHi, selected = i+1, j, value
			}
			i = j
		}
		if len(keys) > 0 && selectedLo >= 0 {
			if selected.Head != "" {
				if !isYAMLFlowMap(selected.Head) {
					return nil, fmt.Errorf("%s must be a YAML mapping", keys[0])
				}
				return yamlFlowSourceMap(source, selected.Start, selected.Start+len(selected.Head), keys[1:])
			}
			if selectedHi == selectedLo {
				return map[string]YAMLSourceValue{}, nil
			}
			return walk(selectedLo, selectedHi, keys[1:])
		}
		return result, nil
	}
	return walk(0, len(lines), path)
}

func yamlFlowSourceMap(source string, start, end int, path []string) (map[string]YAMLSourceValue, error) {
	result := map[string]YAMLSourceValue{}
	inner := source[start+1 : end-1]
	if strings.TrimSpace(inner) == "" {
		return result, nil
	}
	entries, err := splitYAMLFlowEntries(inner)
	if err != nil {
		return nil, err
	}
	offset := start + 1
	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		relative := strings.Index(source[offset:end-1], entry)
		if relative < 0 {
			return nil, fmt.Errorf("cannot locate YAML flow entry")
		}
		entryStart := offset + relative
		key, raw, ok := splitYAMLFlowKeyValue(entry)
		if !ok {
			return nil, fmt.Errorf("invalid YAML flow entry")
		}
		key = strings.TrimSpace(key)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate YAML mapping key %q", key)
		}
		tokenEnd := entryStart + len(strings.TrimRight(entry, " \t"))
		tokenStart := tokenEnd - len(raw)
		value := YAMLSourceValue{Start: tokenStart, End: tokenEnd, Head: raw, Flow: true}
		result[key] = value
		offset = entryStart + len(entry)
	}
	if len(path) > 0 {
		value, ok := result[path[0]]
		if !ok {
			return map[string]YAMLSourceValue{}, nil
		}
		if !isYAMLFlowMap(value.Head) {
			return nil, fmt.Errorf("%s must be a YAML mapping", path[0])
		}
		return yamlFlowSourceMap(source, value.Start, value.End, path[1:])
	}
	return result, nil
}

// ReplaceYAMLSourceValues changes only the located values. Callers validate the
// resulting semantic tree before publication; untouched bytes remain identical.
func ReplaceYAMLSourceValues(source string, values map[string]YAMLSourceValue, replacements map[string]YAMLSourceValue) (string, error) {
	newline := "\n"
	if strings.Contains(source, "\r\n") {
		newline = "\r\n"
	}
	type change struct {
		start, end int
		text       string
	}
	changes := []change{}
	for key, replacement := range replacements {
		current, ok := values[key]
		if !ok {
			return "", fmt.Errorf("candidate must contain a placeholder for preserved key %q", key)
		}
		text, err := current.WithValue(replacement, newline)
		if err != nil {
			return "", err
		}
		changes = append(changes, change{current.Start, current.End, text})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].start > changes[j].start })
	for _, c := range changes {
		source = source[:c.start] + c.text + source[c.end:]
	}
	return source, nil
}
