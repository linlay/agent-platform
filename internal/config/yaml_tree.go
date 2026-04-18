package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type yamlLine struct {
	indent int
	text   string
}

func LoadYAMLTree(path string) (any, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return LoadYAMLTreeReader(file)
}

func LoadYAMLTreeBytes(data []byte) (any, error) {
	return LoadYAMLTreeReader(bytes.NewReader(data))
}

func LoadYAMLTreeReader(reader io.Reader) (any, error) {
	if reader == nil {
		return map[string]any{}, nil
	}

	lines := make([]yamlLine, 0, 64)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), "\r\n")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		trimmed := stripInlineComment(raw)
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := countIndent(trimmed)
		lines = append(lines, yamlLine{
			indent: indent,
			text:   strings.TrimSpace(trimmed),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return map[string]any{}, nil
	}

	node, next, err := parseYAMLBlock(lines, 0, lines[0].indent)
	if err != nil {
		return nil, err
	}
	if next != len(lines) {
		return nil, fmt.Errorf("unexpected trailing yaml content at line %d", next+1)
	}
	return node, nil
}

func parseYAMLBlock(lines []yamlLine, start int, indent int) (any, int, error) {
	if start >= len(lines) {
		return map[string]any{}, start, nil
	}
	if lines[start].indent != indent {
		return nil, start, fmt.Errorf("invalid yaml indentation at line %d", start+1)
	}
	if strings.HasPrefix(lines[start].text, "- ") || lines[start].text == "-" {
		return parseYAMLList(lines, start, indent)
	}
	return parseYAMLMap(lines, start, indent)
}

func parseYAMLMap(lines []yamlLine, start int, indent int) (map[string]any, int, error) {
	result := map[string]any{}
	i := start
	for i < len(lines) {
		line := lines[i]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, i, fmt.Errorf("unexpected nested yaml map line at %d", i+1)
		}
		if strings.HasPrefix(line.text, "- ") || line.text == "-" {
			return nil, i, fmt.Errorf("unexpected list item inside yaml map at line %d", i+1)
		}

		key, rawValue, hasValue := splitYAMLKeyValue(line.text)
		if key == "" {
			return nil, i, fmt.Errorf("invalid yaml key at line %d", i+1)
		}
		if hasValue {
			result[key] = parseYAMLScalar(rawValue)
			i++
			continue
		}

		if i+1 < len(lines) && lines[i+1].indent > indent {
			child, next, err := parseYAMLBlock(lines, i+1, lines[i+1].indent)
			if err != nil {
				return nil, i, err
			}
			result[key] = child
			i = next
			continue
		}

		// Handle YAML shorthand where list items sit at the same indent as
		// their parent key (e.g. "backends:\n- item" without extra indent).
		// This is valid YAML — the list is the value of the key.
		if i+1 < len(lines) && lines[i+1].indent == indent &&
			(strings.HasPrefix(lines[i+1].text, "- ") || lines[i+1].text == "-") {
			child, next, err := parseYAMLList(lines, i+1, indent)
			if err != nil {
				return nil, i, err
			}
			result[key] = child
			i = next
			continue
		}

		result[key] = map[string]any{}
		i++
	}
	return result, i, nil
}

func parseYAMLList(lines []yamlLine, start int, indent int) ([]any, int, error) {
	result := make([]any, 0, 8)
	i := start
	for i < len(lines) {
		line := lines[i]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, i, fmt.Errorf("unexpected nested yaml list line at %d", i+1)
		}
		if !strings.HasPrefix(line.text, "- ") && line.text != "-" {
			break
		}

		itemText := strings.TrimSpace(strings.TrimPrefix(line.text, "-"))
		switch {
		case itemText == "":
			if i+1 < len(lines) && lines[i+1].indent > indent {
				child, next, err := parseYAMLBlock(lines, i+1, lines[i+1].indent)
				if err != nil {
					return nil, i, err
				}
				result = append(result, child)
				i = next
			} else {
				result = append(result, "")
				i++
			}
		case isYAMLFlowMap(itemText):
			itemMap, err := parseYAMLFlowMap(itemText)
			if err != nil {
				return nil, i, err
			}
			result = append(result, itemMap)
			i++
		case looksLikeYAMLMapEntry(itemText):
			key, rawValue, hasValue := splitYAMLKeyValue(itemText)
			itemMap := map[string]any{}
			if hasValue {
				itemMap[key] = parseYAMLScalar(rawValue)
			} else {
				itemMap[key] = map[string]any{}
			}
			i++
			if i < len(lines) && lines[i].indent > indent {
				extra, next, err := parseYAMLMap(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				for extraKey, extraValue := range extra {
					itemMap[extraKey] = extraValue
				}
				i = next
			}
			result = append(result, itemMap)
		default:
			result = append(result, parseYAMLScalar(itemText))
			i++
		}
	}
	return result, i, nil
}

func splitYAMLKeyValue(line string) (string, string, bool) {
	inSingle := false
	inDouble := false
	for i, ch := range line {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if inSingle || inDouble {
				continue
			}
			if i+1 < len(line) && line[i+1] != ' ' {
				continue
			}
			key := strings.TrimSpace(line[:i])
			value := strings.TrimSpace(line[i+1:])
			return key, value, value != ""
		}
	}
	return strings.TrimSpace(line), "", false
}

func parseYAMLScalar(raw string) any {
	value := strings.TrimSpace(raw)
	if isYAMLFlowMap(value) {
		if mapped, err := parseYAMLFlowMap(value); err == nil {
			return mapped
		}
	}
	value = interpolateEnvValue(strings.Trim(value, `"'`))
	lower := strings.ToLower(value)
	switch lower {
	case "[]":
		return []any{}
	case "{}":
		return map[string]any{}
	case "true":
		return true
	case "false":
		return false
	case "null", "~":
		return nil
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil && strings.Contains(value, ".") {
		return f
	}
	return value
}

func countIndent(raw string) int {
	count := 0
	for _, ch := range raw {
		if ch != ' ' {
			break
		}
		count++
	}
	return count
}

func isYAMLFlowMap(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}")
}

func parseYAMLFlowMap(raw string) (map[string]any, error) {
	text := strings.TrimSpace(raw)
	if !isYAMLFlowMap(text) {
		return nil, fmt.Errorf("invalid yaml flow map")
	}
	inner := strings.TrimSpace(text[1 : len(text)-1])
	if inner == "" {
		return map[string]any{}, nil
	}

	entries, err := splitYAMLFlowEntries(inner)
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(entries))
	for _, entry := range entries {
		key, value, ok := splitYAMLFlowKeyValue(entry)
		if !ok {
			return nil, fmt.Errorf("invalid yaml flow map entry %q", entry)
		}
		result[strings.TrimSpace(key)] = parseYAMLScalar(value)
	}
	return result, nil
}

func splitYAMLFlowEntries(raw string) ([]string, error) {
	var parts []string
	start := 0
	inSingle := false
	inDouble := false
	depth := 0

	for i, ch := range raw {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '{', '[':
			if !inSingle && !inDouble {
				depth++
			}
		case '}', ']':
			if !inSingle && !inDouble {
				if depth == 0 {
					return nil, fmt.Errorf("unexpected yaml flow map delimiter")
				}
				depth--
			}
		case ',':
			if !inSingle && !inDouble && depth == 0 {
				part := strings.TrimSpace(raw[start:i])
				if part == "" {
					return nil, fmt.Errorf("empty yaml flow map entry")
				}
				parts = append(parts, part)
				start = i + 1
			}
		}
	}
	if inSingle || inDouble || depth != 0 {
		return nil, fmt.Errorf("unterminated yaml flow map")
	}
	last := strings.TrimSpace(raw[start:])
	if last == "" {
		return nil, fmt.Errorf("empty yaml flow map entry")
	}
	parts = append(parts, last)
	return parts, nil
}

func splitYAMLFlowKeyValue(raw string) (string, string, bool) {
	inSingle := false
	inDouble := false
	depth := 0
	for i, ch := range raw {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '{', '[':
			if !inSingle && !inDouble {
				depth++
			}
		case '}', ']':
			if !inSingle && !inDouble && depth > 0 {
				depth--
			}
		case ':':
			if inSingle || inDouble || depth != 0 {
				continue
			}
			key := strings.TrimSpace(raw[:i])
			value := strings.TrimSpace(raw[i+1:])
			return key, value, key != "" && value != ""
		}
	}
	return "", "", false
}

func looksLikeYAMLMapEntry(text string) bool {
	key, _, _ := splitYAMLKeyValue(text)
	return key != "" && strings.Contains(text, ":")
}
