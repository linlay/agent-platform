package memory

import (
	"fmt"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func extractLearnedMemories(input LearnInput) []api.StoredMemoryResponse {
	text := extractTraceAssistantText(input.Trace)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         generateMemoryID(),
		RequestID:  input.Request.RequestID,
		ChatID:     input.Request.ChatID,
		AgentKey:   strings.TrimSpace(input.AgentKey),
		SubjectKey: normalizeSubjectKey("", input.Request.ChatID, input.AgentKey),
		Kind:       KindObservation,
		RefID:      strings.TrimSpace(input.Trace.RunID),
		ScopeType:  ScopeChat,
		ScopeKey:   observationScopeKey(input),
		Title:      summarizeObservationTitle(text),
		Summary:    strings.TrimSpace(text),
		SourceType: "learn",
		Category:   classifyObservationCategory(text),
		Importance: 8,
		Confidence: 0.75,
		Status:     StatusOpen,
		Tags:       normalizeTags(extractLearnedTags(text)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	return []api.StoredMemoryResponse{normalizeStoredItem(item)}
}

func extractTraceAssistantText(trace chat.RunTrace) string {
	if strings.TrimSpace(trace.AssistantText) != "" {
		return strings.TrimSpace(trace.AssistantText)
	}
	for i := len(trace.Steps) - 1; i >= 0; i-- {
		step := trace.Steps[i]
		for j := len(step.Messages) - 1; j >= 0; j-- {
			message := step.Messages[j]
			if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
				continue
			}
			text := extractMessageText(message)
			if strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func extractMessageText(message chat.StoredMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(part.Text))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractLearnedTags(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	tags := []string{"learned"}
	if strings.Contains(text, "fix") || strings.Contains(text, "bug") {
		tags = append(tags, "bugfix")
	}
	if strings.Contains(text, "scope") {
		tags = append(tags, "scope")
	}
	return tags
}

func learnedDetail(stored []api.StoredMemoryResponse) string {
	return fmt.Sprintf("learned %d memory item(s) from run trace", len(stored))
}
