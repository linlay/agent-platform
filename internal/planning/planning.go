package planning

import (
	"path/filepath"
	"strings"
	"unicode"
)

type Spec struct {
	Title                  string
	Summary                string
	PublicEventsAndStorage []string
	ImplementationChanges  []string
	Interfaces             []string
	TestPlan               []string
	Assumptions            []string
}

func SpecFromArgs(args map[string]any, requestMessage string) Spec {
	title := strings.TrimSpace(anyString(args["title"]))
	if title == "" {
		title = TitleFromRequest(requestMessage)
	}
	if title == "" {
		title = "CODER Planning"
	}
	summary := strings.TrimSpace(anyString(args["summary"]))
	if summary == "" {
		summary = "Create and confirm an execution plan for the user request."
	}
	return Spec{
		Title:                  title,
		Summary:                summary,
		PublicEventsAndStorage: StringList(args["publicEventsAndStorage"]),
		ImplementationChanges:  StringList(args["implementationChanges"]),
		Interfaces:             StringList(args["interfaces"]),
		TestPlan:               StringList(args["testPlan"]),
		Assumptions:            StringList(args["assumptions"]),
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

func StringList(raw any) []string {
	if raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case string:
		return splitLines(value)
	case []string:
		return cleanLines(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				out = append(out, typed)
			case map[string]any:
				out = append(out, mapListItem(typed))
			}
		}
		return cleanLines(out)
	default:
		return nil
	}
}

func RenderMarkdown(spec Spec) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(strings.TrimSpace(spec.Title))
	b.WriteString("\n\n## Summary\n")
	b.WriteString(strings.TrimSpace(spec.Summary))
	b.WriteString("\n\n")
	writeSection(&b, "Public Events And Storage", spec.PublicEventsAndStorage)
	writeSection(&b, "Implementation Changes", spec.ImplementationChanges)
	writeSection(&b, "Interfaces", spec.Interfaces)
	writeSection(&b, "Test Plan", spec.TestPlan)
	writeSection(&b, "Assumptions", spec.Assumptions)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func RenderDraftMarkdown(args map[string]any) string {
	title := strings.TrimSpace(anyString(args["title"]))
	if title == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if !appendDraftSummary(&b, args) {
		return b.String()
	}
	if !appendDraftSection(&b, "Public Events And Storage", args, "publicEventsAndStorage") {
		return b.String()
	}
	if !appendDraftSection(&b, "Implementation Changes", args, "implementationChanges") {
		return b.String()
	}
	if !appendDraftSection(&b, "Interfaces", args, "interfaces") {
		return b.String()
	}
	if !appendDraftSection(&b, "Test Plan", args, "testPlan") {
		return b.String()
	}
	if !appendDraftSection(&b, "Assumptions", args, "assumptions") {
		return b.String()
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func PlanningID(title string, runID string) string {
	return SafeRunID(runID) + "_planning"
}

func PlanningFile(chatsDir string, planningID string) string {
	return filepath.Join(strings.TrimSpace(chatsDir), "plans", strings.TrimSpace(planningID)+".md")
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

func appendDraftSummary(b *strings.Builder, args map[string]any) bool {
	raw, ok := args["summary"]
	if !ok {
		return false
	}
	summary := strings.TrimSpace(anyString(raw))
	if summary == "" {
		return false
	}
	b.WriteString("## Summary\n")
	b.WriteString(summary)
	b.WriteString("\n\n")
	return true
}

func appendDraftSection(b *strings.Builder, title string, args map[string]any, key string) bool {
	raw, ok := args[key]
	if !ok {
		return false
	}
	lines := StringList(raw)
	writeSection(b, title, lines)
	return true
}

func writeSection(b *strings.Builder, title string, lines []string) {
	b.WriteString("## ")
	b.WriteString(title)
	b.WriteByte('\n')
	if len(lines) == 0 {
		b.WriteString("- None specified.\n\n")
		return
	}
	for _, line := range lines {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(line))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func splitLines(value string) []string {
	lines := strings.Split(value, "\n")
	return cleanLines(lines)
}

func cleanLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func mapListItem(item map[string]any) string {
	title := strings.TrimSpace(anyString(item["title"]))
	if title == "" {
		title = strings.TrimSpace(anyString(item["name"]))
	}
	description := strings.TrimSpace(anyString(item["description"]))
	if description == "" {
		description = strings.TrimSpace(anyString(item["text"]))
	}
	if title != "" && description != "" {
		return title + ": " + description
	}
	if title != "" {
		return title
	}
	return description
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
