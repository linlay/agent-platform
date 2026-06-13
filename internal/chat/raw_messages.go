package chat

import (
	"strings"

	"agent-platform/internal/api"
)

// LoadRawMessages loads conversation history from {chatId}.jsonl step lines.
func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = 20
	}

	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	messages := rawMessagesFromJSONLLines(lines)
	if len(messages) == 0 {
		return nil, nil
	}
	if hasActiveCompactCheckpoint(lines) {
		return messages, nil
	}

	// Group by runId, keep last K runs (sliding window)
	type runBucket struct {
		runID    string
		messages []map[string]any
	}
	var runs []*runBucket
	runIndex := map[string]*runBucket{}
	for _, msg := range messages {
		runID, _ := msg["runId"].(string)
		if runID == "" {
			bucket := &runBucket{messages: []map[string]any{msg}}
			runs = append(runs, bucket)
			continue
		}
		bucket, ok := runIndex[runID]
		if !ok {
			bucket = &runBucket{runID: runID}
			runIndex[runID] = bucket
			runs = append(runs, bucket)
		}
		bucket.messages = append(bucket.messages, msg)
	}
	if len(runs) > k {
		runs = runs[len(runs)-k:]
	}
	var result []map[string]any
	for _, bucket := range runs {
		result = append(result, bucket.messages...)
	}
	return result, nil
}

// loadRawMessagesFromJSONL extracts OpenAI-format messages from step lines.
func (s *FileStore) loadRawMessagesFromJSONL(chatID string) []map[string]any {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil
	}
	return rawMessagesFromJSONLLines(lines)
}

func rawMessagesFromJSONLLines(lines []map[string]any) []map[string]any {
	if !isNewFormat(lines) {
		return nil
	}

	var messages []map[string]any
	for _, line := range lines {
		if lineIsCompacted(line) {
			continue
		}
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case CompactCheckpointLineType:
			summary, ok := activeCompactCheckpointSummary(line)
			if !ok {
				continue
			}
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": compactCheckpointSummaryMessage(summary),
				"ts":      line["updatedAt"],
			})
		case "query":
			query, _ := line["query"].(map[string]any)
			if query == nil {
				continue
			}
			role, content := api.ProviderSafeQueryMessage(stringValue(query["role"]), stringValue(query["message"]))
			msg := map[string]any{
				"runId":   runID,
				"role":    role,
				"content": content,
				"ts":      line["updatedAt"],
			}
			messages = append(messages, msg)

		case StepLineTypeLegacyStep, StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute:
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
		}
	}
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
