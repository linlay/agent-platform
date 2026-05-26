package chat

import (
	"strings"
)

// LoadRawMessages loads conversation history from {chatId}.jsonl step lines.
func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	messages := rawMessagesWithCompactProjection(lines, k)
	if len(messages) == 0 {
		return nil, nil
	}
	return messages, nil
}

// loadRawMessagesFromJSONL extracts OpenAI-format messages from step lines.
func (s *FileStore) loadRawMessagesFromJSONL(chatID string) []map[string]any {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil
	}
	return rawMessagesWithCompactProjection(lines, 20)
}

func rawMessagesFromJSONLLines(lines []map[string]any) []map[string]any {
	if !isNewFormat(lines) {
		return nil
	}

	var messages []map[string]any
	var pendingApprovalSummaries []map[string]any
	currentRunID := ""
	flushPendingApprovalSummaries := func() {
		if len(pendingApprovalSummaries) == 0 {
			return
		}
		messages = append(messages, pendingApprovalSummaries...)
		pendingApprovalSummaries = nil
	}

	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)

		if currentRunID == "" {
			currentRunID = runID
		} else if runID != "" && runID != currentRunID {
			flushPendingApprovalSummaries()
			currentRunID = runID
		}

		switch lineType {
		case "query":
			flushPendingApprovalSummaries()
			currentRunID = runID
			query, _ := line["query"].(map[string]any)
			if query == nil {
				continue
			}
			msg := map[string]any{
				"runId":   runID,
				"role":    stringValue(query["role"]),
				"content": stringValue(query["message"]),
				"ts":      line["updatedAt"],
			}
			messages = append(messages, msg)

		case "step", "react", "plan-execute":
			if strings.TrimSpace(stringValue(line["taskSubAgentKey"])) != "" {
				continue
			}
			rawMsgs, _ := line["messages"].([]any)
			for _, raw := range rawMsgs {
				m, _ := raw.(map[string]any)
				if m == nil {
					continue
				}
				role, _ := m["role"].(string)
				msg := map[string]any{"runId": runID}
				for k, v := range m {
					msg[k] = v
				}
				// Flatten content parts to plain text for LLM context
				if role == "user" || role == "assistant" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
					if parts, ok := m["reasoning_content"].([]any); ok {
						msg["reasoning_content"] = extractTextFromContent(parts)
					}
				}
				if role == "tool" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
				}
				messages = append(messages, msg)
			}
			if approval, ok := line["approval"].(map[string]any); ok {
				notice := stringValue(approval["llmNotice"])
				if notice == "" {
					notice = stringValue(approval["summary"])
				}
				if notice != "" {
					pendingApprovalSummaries = append(pendingApprovalSummaries, map[string]any{
						"runId":   runID,
						"role":    "user",
						"content": notice,
						"ts":      line["updatedAt"],
					})
				}
			}
		}
	}
	flushPendingApprovalSummaries()
	return messages
}

func extractStoredMessageText(message StoredMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
