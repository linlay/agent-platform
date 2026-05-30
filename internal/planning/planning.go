package planning

import (
	"path/filepath"
	"strings"
	"unicode"

	"agent-platform/internal/chat"
)

type Spec struct {
	Markdown string
}

func SpecFromArgs(args map[string]any) Spec {
	return Spec{
		Markdown: anyString(args["markdown"]),
	}
}

func RenderMarkdown(spec Spec) string {
	return spec.Markdown
}

func ValidMarkdown(markdown string) bool {
	return strings.TrimSpace(markdown) != ""
}

func PlanningIDForRevision(runID string, revision int) string {
	if revision <= 0 {
		revision = 1
	}
	return SafeRunID(runID) + "_planning_" + intString(revision)
}

func PlanningFile(chatsDir string, planningID string) string {
	return filepath.Join(strings.TrimSpace(chatsDir), "plans", strings.TrimSpace(planningID)+".md")
}

func PlanningFileForChat(chatsDir string, chatID string, planningID string) string {
	chatsDir = strings.TrimSpace(chatsDir)
	chatID = strings.TrimSpace(chatID)
	if chatsDir == "" || !chat.ValidChatID(chatID) {
		return PlanningFile(chatsDir, planningID)
	}
	return filepath.Join(chatsDir, chatID, chat.ToolRootDirName, chat.ToolPlansDirName, strings.TrimSpace(planningID)+".md")
}

func SafeRunID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "run"
	}
	return out
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func intString(value int) string {
	if value <= 0 {
		return "1"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
