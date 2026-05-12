package chat

import (
	"path/filepath"
	"strings"
)

// LoadRawMessages loads conversation history from {chatId}.jsonl step lines,
// falling back to {chatId}/raw_messages.jsonl for old chats.
func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = 20
	}

	// Try loading from step lines in {chatId}.jsonl (Java-compatible path)
	messages := s.loadRawMessagesFromJSONL(chatID)
	if len(messages) == 0 {
		// Fallback to old raw_messages.jsonl
		var err error
		messages, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
		if err != nil || len(messages) == 0 {
			return nil, err
		}
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
				if summary := stringValue(approval["summary"]); summary != "" {
					pendingApprovalSummaries = append(pendingApprovalSummaries, map[string]any{
						"runId":   runID,
						"role":    "user",
						"content": summary,
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
