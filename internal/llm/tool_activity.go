package llm

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/credentialview"
)

const toolActivityTextLimit = 60

// runActivity turns a progress point into the activity template the stream
// publishes. A tool that starts running shows its label plus the first
// non-empty argument its definition lists in activityArgs.
func (m *DeltaMapper) runActivity(value DeltaRunActivity) map[string]any {
	switch value.Kind {
	case ActivityThinking:
		return map[string]any{"kind": ActivityThinking}
	case ActivityToolRun:
	default:
		return nil
	}
	label, _ := m.resolveToolMetadata(value.ToolName)
	activity := map[string]any{
		"kind":      ActivityToolRun,
		"toolId":    value.ToolID,
		"toolName":  value.ToolName,
		"toolLabel": label,
	}
	if translations := m.resolveToolTranslations(value.ToolName); len(translations) > 0 {
		activity["toolI18n"] = translations
	}
	if text := m.toolActivityText(value.ToolName, value.ToolArgs); text != "" {
		activity["text"] = text
	}
	return activity
}

func (m *DeltaMapper) toolActivityText(toolName string, args map[string]any) string {
	if m.toolRegistry == nil || len(args) == 0 || credentialview.IsCatalogTool(toolName) {
		return ""
	}
	tool, ok := m.toolRegistry.Tool(toolName)
	if !ok {
		return ""
	}
	for _, name := range ToolActivityArgs(tool.Meta) {
		if text, _ := args[name].(string); strings.TrimSpace(text) != "" {
			return toolActivityDisplay(text)
		}
	}
	return ""
}

// toolActivityDisplay keeps one short line: a URL loses its query and
// fragment, an absolute path shows only its file name.
func toolActivityDisplay(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		value = parsed.Host + strings.TrimSuffix(parsed.EscapedPath(), "/")
	} else if filepath.IsAbs(value) && !strings.Contains(value, " ") {
		value = filepath.Base(value)
	}
	if utf8.RuneCountInString(value) > toolActivityTextLimit {
		runes := []rune(value)
		value = string(runes[:toolActivityTextLimit-1]) + "…"
	}
	return value
}
