package planning

import (
	"path/filepath"
	"strings"
	"unicode"

	"agent-platform/internal/chat"
)

type Spec struct {
	Title    string
	Markdown string
}

func SpecFromArgs(args map[string]any, requestMessage string) Spec {
	title := strings.TrimSpace(anyString(args["title"]))
	if title == "" {
		title = TitleFromRequest(requestMessage)
	}
	if title == "" {
		title = "CODER Planning"
	}
	return Spec{
		Title:    title,
		Markdown: NormalizeMarkdown(anyString(args["markdown"]), title),
	}
}

func TitleFromRequest(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	runes := []rune(message)
	if len(runes) > 40 {
		message = strings.TrimSpace(string(runes[:40]))
	}
	return message
}

func RenderMarkdown(spec Spec) string {
	return NormalizeMarkdown(spec.Markdown, spec.Title)
}

func RenderDraftMarkdown(args map[string]any) string {
	title := strings.TrimSpace(anyString(args["title"]))
	if title == "" {
		return ""
	}
	return NormalizeMarkdown(anyString(args["markdown"]), title)
}

func NormalizeMarkdown(markdown string, title string) string {
	markdown = strings.TrimSpace(markdown)
	title = strings.TrimSpace(title)
	if markdown == "" {
		if title == "" {
			return ""
		}
		return "# " + title + "\n"
	}
	if !strings.HasPrefix(strings.TrimSpace(markdown), "#") && title != "" {
		markdown = "# " + title + "\n\n" + markdown
	}
	return strings.TrimRight(markdown, "\n") + "\n"
}

func PlanningID(title string, runID string) string {
	return PlanningIDForRevision(runID, 1)
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

func SafeFileStem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "planning"
	}
	var b strings.Builder
	lastDash := false
	count := 0
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			count++
		} else if r == '_' {
			b.WriteRune(r)
			lastDash = false
			count++
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
		if count >= 80 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "planning"
	}
	return out
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
