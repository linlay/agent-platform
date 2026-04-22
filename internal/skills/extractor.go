package skills

import (
	"regexp"
	"strings"

	"agent-platform-runner-go/internal/chat"
)

func CandidateFromRunTrace(trace chat.RunTrace, agentKey string, chatID string) (CandidateInput, bool) {
	text := strings.TrimSpace(trace.AssistantText)
	if text == "" {
		for i := len(trace.Steps) - 1; i >= 0 && text == ""; i-- {
			for j := len(trace.Steps[i].Messages) - 1; j >= 0; j-- {
				msg := trace.Steps[i].Messages[j]
				if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
					continue
				}
				for _, part := range msg.Content {
					if strings.TrimSpace(part.Text) != "" {
						text = strings.TrimSpace(part.Text)
						break
					}
				}
				if text != "" {
					break
				}
			}
		}
	}
	if !looksProcedural(text) {
		return CandidateInput{}, false
	}
	return CandidateInput{
		AgentKey:        strings.TrimSpace(agentKey),
		ChatID:          strings.TrimSpace(chatID),
		RunID:           strings.TrimSpace(trace.RunID),
		SourceKind:      "learn",
		Title:           summarizeProcedureTitle(text),
		Summary:         summarizeProcedureSummary(text),
		Procedure:       text,
		Intent:          summarizeWorkflowIntent(trace, text),
		Preconditions:   extractWorkflowPreconditions(text),
		Steps:           extractWorkflowSteps(text),
		FailurePatterns: extractWorkflowFailurePatterns(text),
		SuccessCriteria: extractWorkflowSuccessCriteria(text),
		Category:        classifyProcedureCategory(text),
		Confidence:      0.72,
		Tags:            procedureTags(text),
	}, true
}

func CandidateFromObservation(agentKey string, sourceMemoryID string, summary string, category string, confidence float64) (CandidateInput, bool) {
	if !looksProcedural(summary) {
		return CandidateInput{}, false
	}
	return CandidateInput{
		AgentKey:        strings.TrimSpace(agentKey),
		SourceKind:      "consolidate",
		SourceMemoryID:  strings.TrimSpace(sourceMemoryID),
		Title:           summarizeProcedureTitle(summary),
		Summary:         summarizeProcedureSummary(summary),
		Procedure:       strings.TrimSpace(summary),
		Intent:          summarizeProcedureSummary(summary),
		Preconditions:   extractWorkflowPreconditions(summary),
		Steps:           extractWorkflowSteps(summary),
		FailurePatterns: extractWorkflowFailurePatterns(summary),
		SuccessCriteria: extractWorkflowSuccessCriteria(summary),
		Category:        normalizeText(category, "workflow"),
		Confidence:      normalizeConfidence(confidence),
		Tags:            procedureTags(summary),
	}, true
}

func looksProcedural(text string) bool {
	needle := strings.ToLower(strings.TrimSpace(text))
	if needle == "" {
		return false
	}
	keywords := []string{
		"step", "steps", "run ", "first ", "then ", "finally ", "workflow",
		"procedure", "checklist", "playbook", "how to", "use ", "before merge",
	}
	for _, keyword := range keywords {
		if strings.Contains(needle, keyword) {
			return true
		}
	}
	return false
}

func summarizeProcedureTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Procedure Candidate"
	}
	if len([]rune(text)) <= 72 {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:72])) + "..."
}

func summarizeProcedureSummary(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= 160 {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:160])) + "..."
}

func classifyProcedureCategory(text string) string {
	needle := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(needle, "debug") || strings.Contains(needle, "troubleshoot"):
		return "debugging"
	case strings.Contains(needle, "merge") || strings.Contains(needle, "test"):
		return "workflow"
	default:
		return "workflow"
	}
}

func procedureTags(text string) []string {
	tags := []string{"candidate", "procedure"}
	needle := strings.ToLower(strings.TrimSpace(text))
	if strings.Contains(needle, "test") {
		tags = append(tags, "testing")
	}
	if strings.Contains(needle, "debug") {
		tags = append(tags, "debugging")
	}
	return normalizeTags(tags)
}

func summarizeWorkflowIntent(trace chat.RunTrace, text string) string {
	if trace.Query != nil {
		if message, ok := trace.Query.Query["message"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	return summarizeProcedureSummary(text)
}

var sentenceSplitPattern = regexp.MustCompile(`[。\n]+`)

func extractWorkflowSteps(text string) []string {
	parts := splitWorkflowText(text)
	steps := make([]string, 0, len(parts))
	for _, part := range parts {
		lower := strings.ToLower(part)
		if strings.Contains(lower, "first") || strings.Contains(lower, "then") || strings.Contains(lower, "finally") ||
			strings.Contains(lower, "step") || strings.Contains(lower, "verify") || strings.Contains(lower, "check") ||
			strings.Contains(lower, "rollback") || strings.Contains(lower, "retry") {
			steps = append(steps, part)
		}
	}
	if len(steps) == 0 {
		steps = parts
	}
	if len(steps) > 6 {
		steps = steps[:6]
	}
	return normalizeTextList(steps)
}

func extractWorkflowPreconditions(text string) []string {
	parts := splitWorkflowText(text)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		lower := strings.ToLower(part)
		if strings.Contains(lower, "before") || strings.Contains(lower, "ensure") || strings.Contains(lower, "confirm") || strings.Contains(lower, "verify") {
			out = append(out, part)
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return normalizeTextList(out)
}

func extractWorkflowFailurePatterns(text string) []string {
	parts := splitWorkflowText(text)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		lower := strings.ToLower(part)
		if strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "timeout") ||
			strings.Contains(lower, "rollback") || strings.Contains(lower, "retry") || strings.Contains(lower, "if ") {
			out = append(out, part)
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return normalizeTextList(out)
}

func extractWorkflowSuccessCriteria(text string) []string {
	parts := splitWorkflowText(text)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		lower := strings.ToLower(part)
		if strings.Contains(lower, "success") || strings.Contains(lower, "done") || strings.Contains(lower, "verified") ||
			strings.Contains(lower, "healthy") || strings.Contains(lower, "pass") {
			out = append(out, part)
		}
	}
	return normalizeTextList(out)
}

func splitWorkflowText(text string) []string {
	parts := sentenceSplitPattern.Split(strings.TrimSpace(text), -1)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(strings.Trim(trimmedBullet(part), "-*0123456789. "))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return normalizeTextList(out)
}

func trimmedBullet(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "-") || strings.HasPrefix(value, "*") {
		value = strings.TrimSpace(value[1:])
	}
	return value
}
