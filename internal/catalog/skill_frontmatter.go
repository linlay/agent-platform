package catalog

import "strings"

func parseSkillPromptMetadata(prompt string) (string, string, []string, map[string]any) {
	frontMatter, body := parseSkillFrontMatter(prompt)
	name := frontMatterString(frontMatter["name"])
	description := frontMatterString(frontMatter["description"])
	triggers := frontMatterStringSlice(frontMatter["triggers"])
	metadata := frontMatterMap(frontMatter["metadata"])

	heading := firstMarkdownHeading(body)
	firstLine := firstNonEmptyMarkdownLine(body)
	if strings.TrimSpace(description) == "" {
		description = firstLine
	}
	if strings.TrimSpace(name) == "" {
		if strings.TrimSpace(heading) != "" {
			name = heading
		} else {
			name = description
		}
	}
	return strings.TrimSpace(name), strings.TrimSpace(description), triggers, metadata
}

func parseSkillFrontMatter(prompt string) (map[string]any, string) {
	lines := strings.Split(prompt, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, prompt
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, prompt
	}

	parser := skillFrontMatterParser{lines: lines[1:end]}
	values, ok := parser.parseNode(0).(map[string]any)
	if !ok {
		values = nil
	}
	return values, strings.Join(lines[end+1:], "\n")
}

type skillFrontMatterParser struct {
	lines []string
	idx   int
}

func (p *skillFrontMatterParser) parseNode(indent int) any {
	p.skipIgnorable()
	if p.idx >= len(p.lines) {
		return nil
	}
	lineIndent, trimmed, ok := p.peekLine()
	if !ok || lineIndent < indent {
		return nil
	}
	if lineIndent > indent {
		indent = lineIndent
	}
	if strings.HasPrefix(trimmed, "-") {
		return p.parseSequence(indent)
	}
	return p.parseMap(indent)
}

func (p *skillFrontMatterParser) parseMap(indent int) map[string]any {
	result := map[string]any{}
	for {
		p.skipIgnorable()
		lineIndent, trimmed, ok := p.peekLine()
		if !ok || lineIndent < indent {
			break
		}
		if lineIndent != indent || strings.HasPrefix(trimmed, "-") {
			break
		}

		key, rawValue, found := splitFrontMatterKeyValue(trimmed)
		if !found {
			p.idx++
			continue
		}
		p.idx++

		value := strings.TrimSpace(rawValue)
		switch {
		case value == "":
			if nextIndent, ok := p.peekNextContentIndent(); ok && nextIndent > indent {
				result[key] = p.parseNode(nextIndent)
			} else {
				result[key] = ""
			}
		case isBlockScalarToken(value):
			result[key] = p.parseBlockScalar(indent, value[0])
		default:
			result[key] = parseFrontMatterScalar(value)
		}
	}
	return result
}

func (p *skillFrontMatterParser) parseSequence(indent int) []any {
	var items []any
	for {
		p.skipIgnorable()
		lineIndent, trimmed, ok := p.peekLine()
		if !ok || lineIndent < indent || lineIndent != indent || !strings.HasPrefix(trimmed, "-") {
			break
		}

		itemText := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		p.idx++

		switch {
		case itemText == "":
			if nextIndent, ok := p.peekNextContentIndent(); ok && nextIndent > indent {
				items = append(items, p.parseNode(nextIndent))
			} else {
				items = append(items, "")
			}
		case isBlockScalarToken(itemText):
			items = append(items, p.parseBlockScalar(indent, itemText[0]))
		case looksLikeInlineMap(itemText):
			item := map[string]any{}
			key, rawValue, _ := splitFrontMatterKeyValue(itemText)
			value := strings.TrimSpace(rawValue)
			switch {
			case value == "":
				if nextIndent, ok := p.peekNextContentIndent(); ok && nextIndent > indent {
					item[key] = p.parseNode(nextIndent)
				} else {
					item[key] = ""
				}
			case isBlockScalarToken(value):
				item[key] = p.parseBlockScalar(indent, value[0])
			default:
				item[key] = parseFrontMatterScalar(value)
			}
			items = append(items, item)
		default:
			items = append(items, parseFrontMatterScalar(itemText))
		}
	}
	return items
}

func (p *skillFrontMatterParser) parseBlockScalar(parentIndent int, style byte) string {
	var rawLines []string
	scalarIndent := -1
	for p.idx < len(p.lines) {
		line := p.lines[p.idx]
		trimmed := strings.TrimSpace(line)
		lineIndent := countLeadingSpaces(line)
		if trimmed != "" && lineIndent <= parentIndent {
			break
		}
		p.idx++

		if trimmed == "" {
			rawLines = append(rawLines, "")
			continue
		}
		if scalarIndent < 0 {
			scalarIndent = lineIndent
		}
		cut := scalarIndent
		if cut > len(line) {
			cut = len(line)
		}
		rawLines = append(rawLines, line[cut:])
	}

	if style == '|' {
		return strings.TrimSpace(strings.Join(rawLines, "\n"))
	}
	return foldBlockScalar(rawLines)
}

func (p *skillFrontMatterParser) skipIgnorable() {
	for p.idx < len(p.lines) {
		trimmed := strings.TrimSpace(p.lines[p.idx])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			p.idx++
			continue
		}
		return
	}
}

func (p *skillFrontMatterParser) peekLine() (int, string, bool) {
	if p.idx >= len(p.lines) {
		return 0, "", false
	}
	line := p.lines[p.idx]
	return countLeadingSpaces(line), strings.TrimSpace(line), true
}

func (p *skillFrontMatterParser) peekNextContentIndent() (int, bool) {
	for i := p.idx; i < len(p.lines); i++ {
		trimmed := strings.TrimSpace(p.lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return countLeadingSpaces(p.lines[i]), true
	}
	return 0, false
}

func splitFrontMatterKeyValue(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func isBlockScalarToken(value string) bool {
	return strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|")
}

func looksLikeInlineMap(value string) bool {
	key, _, ok := splitFrontMatterKeyValue(value)
	return ok && key != ""
}

func parseFrontMatterScalar(value string) any {
	value = strings.TrimSpace(value)
	switch value {
	case "null", "~":
		return nil
	case "true":
		return true
	case "false":
		return false
	case "[]":
		return []any{}
	case "{}":
		return map[string]any{}
	default:
		return unquoteFrontMatterValue(value)
	}
}

func unquoteFrontMatterValue(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func foldBlockScalar(lines []string) string {
	paragraphs := make([]string, 0, 2)
	current := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(current) > 0 {
				paragraphs = append(paragraphs, strings.Join(current, " "))
				current = current[:0]
			}
			continue
		}
		current = append(current, trimmed)
	}
	if len(current) > 0 {
		paragraphs = append(paragraphs, strings.Join(current, " "))
	}
	return strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
}

func frontMatterString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func frontMatterStringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if value == nil {
			return nil
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return []string{strings.TrimSpace(text)}
		}
		return nil
	}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		items = append(items, text)
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func frontMatterMap(value any) map[string]any {
	raw, ok := value.(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]any, len(raw))
	for key, item := range raw {
		out[key] = cloneFrontMatterValue(item)
	}
	return out
}

func cloneFrontMatterValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneFrontMatterValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneFrontMatterValue(item))
		}
		return out
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	default:
		return typed
	}
}

func countLeadingSpaces(line string) int {
	count := 0
	for count < len(line) && line[count] == ' ' {
		count++
	}
	return count
}

func firstMarkdownHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			return ""
		}
		trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmptyMarkdownLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#-* ")
		if trimmed == "" {
			continue
		}
		return trimmed
	}
	return ""
}
