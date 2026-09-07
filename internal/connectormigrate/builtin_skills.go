package connectormigrate

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

var migrationScopes = []string{"connectors", "agents", "registries/mcp-servers", "skills-center/builtin-dbx", "skills-center/builtin-httpx"}

// migrateBuiltinSkills preserves unrelated YAML bytes and merges the retired
// standalone skill references into the explicit connector mount list.
func migrateBuiltinSkills(data []byte) ([]byte, bool, error) {
	if !strings.Contains(strings.ToLower(string(data)), "builtin-dbx") && !strings.Contains(strings.ToLower(string(data)), "builtin-httpx") {
		return data, false, nil
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		return nil, false, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("agent must be an object")
	}
	skills, _ := root["skillConfig"].(map[string]any)
	var list []any
	switch value := skills["skills"].(type) {
	case nil:
	case []any:
		list = value
	case string:
		list = []any{value}
	default:
		return nil, false, fmt.Errorf("skillConfig.skills must be a skill name or array")
	}
	ordinary, added := []string{}, []string{}
	for _, raw := range list {
		key, ok := raw.(string)
		if !ok {
			return nil, false, fmt.Errorf("skillConfig.skills must contain strings")
		}
		if id := connector.BuiltinSkillConnector(key); id != "" {
			added = append(added, id)
		} else {
			ordinary = append(ordinary, key)
		}
	}
	if len(added) == 0 {
		return data, false, nil
	}
	mounts := []string{}
	seen := map[string]bool{}
	if raw, exists := root["connectorConfig"]; exists {
		section, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("connectorConfig must be an object")
		}
		if raw, exists := section["connectors"]; exists {
			existing, ok := raw.([]any)
			if !ok {
				return nil, false, fmt.Errorf("connectorConfig.connectors must be an array")
			}
			for _, raw := range existing {
				id, ok := raw.(string)
				if !ok || !connector.ValidID(id) {
					return nil, false, fmt.Errorf("invalid connector reference")
				}
				if !seen[id] {
					mounts = append(mounts, id)
					seen[id] = true
				}
			}
		}
	}
	for _, id := range added {
		if !seen[id] {
			mounts = append(mounts, id)
			seen[id] = true
		}
	}
	data, err = replaceYAMLList(data, "skillConfig", "skills", ordinary)
	if err != nil {
		return nil, false, err
	}
	data, err = replaceYAMLList(data, "connectorConfig", "connectors", mounts)
	return data, true, err
}

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
