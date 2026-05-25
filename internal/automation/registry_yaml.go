package automation

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func renderDefinition(def Definition) []byte {
	var b strings.Builder
	writeYAMLKeyValue(&b, 0, "name", def.Name)
	writeYAMLKeyValue(&b, 0, "description", def.Description)
	writeYAMLKeyValue(&b, 0, "enabled", def.Enabled)
	writeYAMLKeyValue(&b, 0, "cron", def.Cron)
	if def.RemainingRuns != nil {
		writeYAMLKeyValue(&b, 0, "remainingRuns", *def.RemainingRuns)
	}
	writeYAMLKeyValue(&b, 0, "agentKey", def.AgentKey)
	if strings.TrimSpace(def.TeamID) != "" {
		writeYAMLKeyValue(&b, 0, "teamId", def.TeamID)
	}
	if strings.TrimSpace(def.Environment.ZoneID) != "" {
		writeYAMLKeyValue(&b, 0, "environment", map[string]any{
			"zoneId": def.Environment.ZoneID,
		})
	}

	writeYAMLLine(&b, 0, "query:")
	if strings.TrimSpace(def.Query.RequestID) != "" {
		writeYAMLKeyValue(&b, 2, "requestId", def.Query.RequestID)
	}
	if strings.TrimSpace(def.Query.ChatID) != "" {
		writeYAMLKeyValue(&b, 2, "chatId", def.Query.ChatID)
	}
	if strings.TrimSpace(def.Query.Role) != "" {
		writeYAMLKeyValue(&b, 2, "role", def.Query.Role)
	}
	writeYAMLKeyValue(&b, 2, "message", def.Query.Message)
	if def.Query.Hidden != nil {
		writeYAMLKeyValue(&b, 2, "hidden", *def.Query.Hidden)
	}
	if len(def.Query.Params) > 0 {
		writeYAMLKeyValue(&b, 2, "params", def.Query.Params)
	}
	if def.Query.Scene != nil {
		scene := map[string]any{}
		if strings.TrimSpace(def.Query.Scene.URL) != "" {
			scene["url"] = def.Query.Scene.URL
		}
		if strings.TrimSpace(def.Query.Scene.Title) != "" {
			scene["title"] = def.Query.Scene.Title
		}
		if len(scene) > 0 {
			writeYAMLKeyValue(&b, 2, "scene", scene)
		}
	}
	if len(def.Query.References) > 0 {
		items := make([]any, 0, len(def.Query.References))
		for _, ref := range def.Query.References {
			node := map[string]any{}
			if strings.TrimSpace(ref.ID) != "" {
				node["id"] = ref.ID
			}
			if strings.TrimSpace(ref.Type) != "" {
				node["type"] = ref.Type
			}
			if strings.TrimSpace(ref.Name) != "" {
				node["name"] = ref.Name
			}
			if strings.TrimSpace(ref.MimeType) != "" {
				node["mimeType"] = ref.MimeType
			}
			if ref.SizeBytes != nil {
				node["sizeBytes"] = *ref.SizeBytes
			}
			if strings.TrimSpace(ref.URL) != "" {
				node["url"] = ref.URL
			}
			if strings.TrimSpace(ref.SHA256) != "" {
				node["sha256"] = ref.SHA256
			}
			if strings.TrimSpace(ref.SandboxPath) != "" {
				node["sandboxPath"] = ref.SandboxPath
			}
			if len(ref.Meta) > 0 {
				node["meta"] = ref.Meta
			}
			items = append(items, node)
		}
		writeYAMLKeyValue(&b, 2, "references", items)
	}

	if strings.TrimSpace(def.PushURL) != "" {
		writeYAMLKeyValue(&b, 0, "pushUrl", def.PushURL)
	}
	if strings.TrimSpace(def.PushTargetID) != "" {
		writeYAMLKeyValue(&b, 0, "pushTargetId", def.PushTargetID)
	}

	return []byte(b.String())
}

func writeYAMLKeyValue(b *strings.Builder, indent int, key string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			writeYAMLLine(b, indent, key+": {}")
			return
		}
		writeYAMLLine(b, indent, key+":")
		writeYAMLMap(b, indent+2, typed)
	case []any:
		if len(typed) == 0 {
			writeYAMLLine(b, indent, key+": []")
			return
		}
		writeYAMLLine(b, indent, key+":")
		writeYAMLList(b, indent+2, typed)
	default:
		writeYAMLLine(b, indent, key+": "+formatYAMLScalar(typed))
	}
}

func writeYAMLMap(b *strings.Builder, indent int, node map[string]any) {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeYAMLKeyValue(b, indent, key, node[key])
	}
}

func writeYAMLList(b *strings.Builder, indent int, items []any) {
	for _, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			if len(typed) == 0 {
				writeYAMLLine(b, indent, "- {}")
				continue
			}
			writeYAMLLine(b, indent, "-")
			writeYAMLMap(b, indent+2, typed)
		case []any:
			if len(typed) == 0 {
				writeYAMLLine(b, indent, "- []")
				continue
			}
			writeYAMLLine(b, indent, "-")
			writeYAMLList(b, indent+2, typed)
		default:
			writeYAMLLine(b, indent, "- "+formatYAMLScalar(typed))
		}
	}
}

func writeYAMLLine(b *strings.Builder, indent int, line string) {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(line)
	b.WriteByte('\n')
}

func formatYAMLScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return quoteYAMLString(typed)
	default:
		return quoteYAMLString(fmt.Sprint(typed))
	}
}

func quoteYAMLString(value string) string {
	sanitized := strings.ReplaceAll(value, "\r\n", "\n")
	sanitized = strings.ReplaceAll(sanitized, "\n", "\\n")
	if canUsePlainYAMLScalar(sanitized) {
		return sanitized
	}
	if !strings.Contains(sanitized, "'") {
		return "'" + sanitized + "'"
	}
	if !strings.Contains(sanitized, `"`) {
		return `"` + sanitized + `"`
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(sanitized, `\`, `\\`), `"`, `\"`) + `"`
}

func canUsePlainYAMLScalar(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	lower := strings.ToLower(value)
	switch lower {
	case "true", "false", "null", "~", "[]", "{}":
		return false
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil && strings.Contains(value, ".") {
		return false
	}
	if strings.Contains(value, ": ") || strings.ContainsAny(value, "#\t") {
		return false
	}
	switch value[0] {
	case '-', '?', ':', '[', ']', '{', '}', ',', '&', '*', '!', '|', '>', '@', '`':
		return false
	}
	return !strings.ContainsAny(value, "\n\r")
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func insideDir(parent string, child string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
