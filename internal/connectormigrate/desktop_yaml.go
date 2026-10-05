package connectormigrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Only the selected list is replaced. Unsupported inline parent maps fail
// before publication instead of rewriting arbitrary configuration or prompts.
func replaceYAMLList(data []byte, section, key string, items []string) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || len(line) != len(strings.TrimLeft(line, " \t")) {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) == section {
			if strings.TrimSpace(value) != "" && !strings.HasPrefix(strings.TrimSpace(value), "#") {
				return nil, fmt.Errorf("unsupported inline %s; expand YAML before migration", section)
			}
			start = i
			continue
		}
		if start >= 0 {
			end = i
			break
		}
	}
	render := func(indent int) []string {
		prefix := strings.Repeat(" ", indent)
		if len(items) == 0 {
			return []string{prefix + key + ": []"}
		}
		result := []string{prefix + key + ":"}
		for _, item := range items {
			quoted, _ := json.Marshal(item)
			result = append(result, prefix+"  - "+string(quoted))
		}
		return result
	}
	if start < 0 {
		return []byte(strings.TrimRight(string(data), "\n") + "\n" + section + ":\n" + strings.Join(render(2), "\n") + "\n"), nil
	}
	childIndent := -1
	listStart, listEnd := -1, end
	for i := start + 1; i < end; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if childIndent < 0 {
			childIndent = indent
		}
		if listStart >= 0 && indent <= childIndent && !strings.HasPrefix(trimmed, "-") {
			listEnd = i
			break
		}
		if indent == childIndent && strings.HasPrefix(trimmed, key+":") {
			listStart = i
		}
	}
	if childIndent < 0 {
		childIndent = 2
	}
	if listStart < 0 {
		listStart, listEnd = end, end
	}
	out := append([]string(nil), lines[:listStart]...)
	out = append(out, render(childIndent)...)
	// Retain standalone comments from the replaced list region.
	for _, line := range lines[listStart:listEnd] {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			out = append(out, line)
		}
	}
	out = append(out, lines[listEnd:]...)
	return []byte(strings.Join(out, "\n")), nil
}
