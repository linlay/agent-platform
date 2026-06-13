package llm

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

func buildInjectedPromptPayload(session QuerySession, req api.QueryRequest, options PromptBuildOptions, messages []openAIMessage) map[string]any {
	if len(messages) == 0 {
		return nil
	}

	lastUserIdx := -1
	for idx, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			lastUserIdx = idx
		}
	}

	providerMessages := make([]any, 0, len(messages))
	historyMessages := make([]any, 0, len(messages))
	var currentUserMessage map[string]any

	for idx, message := range messages {
		normalized := normalizeInjectedPromptMessage(message)
		if len(normalized) == 0 {
			continue
		}
		providerMessages = append(providerMessages, normalized)

		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "system" {
			continue
		}
		if role == "user" && idx == lastUserIdx {
			currentUserMessage = normalized
			continue
		}
		historyMessages = append(historyMessages, normalized)
	}

	systemSections := buildInjectedSystemSections(session, req, options)
	systemPrompt := ""
	if len(systemSections) > 0 {
		parts := make([]string, 0, len(systemSections))
		for _, section := range systemSections {
			content := strings.TrimSpace(stringValue(section["content"]))
			if content != "" {
				parts = append(parts, content)
			}
		}
		systemPrompt = strings.Join(parts, "\n\n")
	}

	payload := map[string]any{
		"providerMessages":       providerMessages,
		"providerMessagesTokens": estimateTokensFromValue(providerMessages),
	}
	if systemPrompt != "" {
		payload["systemPrompt"] = systemPrompt
		payload["systemPromptTokens"] = estimateTokensFromText(systemPrompt)
	}
	if len(systemSections) > 0 {
		payload["systemSections"] = systemSections
	}
	if len(historyMessages) > 0 {
		payload["historyMessages"] = historyMessages
		payload["historyMessagesTokens"] = estimateTokensFromValue(historyMessages)
	}
	if currentUserMessage != nil {
		payload["currentUserMessage"] = currentUserMessage
		payload["currentUserMessageTokens"] = estimateTokensFromValue(currentUserMessage)
	}
	return payload
}

func buildInjectedSystemSections(session QuerySession, req api.QueryRequest, options PromptBuildOptions) []map[string]any {
	source := buildSystemPromptSections(session, req, options)
	sections := make([]map[string]any, 0, len(source))
	for _, section := range source {
		sections = append(sections, map[string]any{
			"id":       section.ID,
			"title":    section.Title,
			"role":     "system",
			"category": section.Category,
			"content":  section.Content,
			"tokens":   estimateTokensFromText(section.Content),
		})
	}
	return sections
}

func normalizeInjectedPromptMessage(message openAIMessage) map[string]any {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		return nil
	}

	normalized := map[string]any{
		"role": role,
	}
	if content := strings.TrimSpace(debugPromptContentText(message.Content)); content != "" {
		normalized["content"] = content
	}
	if strings.TrimSpace(message.Name) != "" {
		normalized["name"] = strings.TrimSpace(message.Name)
	}
	if strings.TrimSpace(message.ToolCallID) != "" {
		normalized["toolCallId"] = strings.TrimSpace(message.ToolCallID)
	}
	if len(message.ToolCalls) > 0 {
		toolCalls := make([]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":   call.ID,
				"type": call.Type,
				"function": map[string]any{
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				},
			})
		}
		normalized["toolCalls"] = toolCalls
	}
	normalized["estimatedTokens"] = estimateTokensFromOpenAIMessage(message)
	return normalized
}

func debugPromptContentText(content any) string {
	switch typed := content.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []string:
		return strings.Join(typed, "\n\n")
	default:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func estimateTokensFromOpenAIMessage(message openAIMessage) int {
	raw, err := json.Marshal(message)
	if err != nil {
		return 0
	}
	return estimateTokensFromBytes(len(raw))
}

func estimateTokensFromValue(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return estimateTokensFromBytes(len(raw))
}

func estimateTokensFromText(text string) int {
	return estimateTokensFromBytes(len([]byte(text)))
}

func estimateTokensFromBytes(byteCount int) int {
	if byteCount <= 0 {
		return 0
	}
	return (byteCount + 3) / 4
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
