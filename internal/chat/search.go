package chat

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

type SearchHit struct {
	Kind      string         `json:"kind"`
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId,omitempty"`
	Stage     string         `json:"stage,omitempty"`
	Role      string         `json:"role,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Snippet   string         `json:"snippet"`
	Score     int            `json:"score"`
	Meta      map[string]any `json:"meta,omitempty"`
}

func (s *FileStore) SearchSession(chatID string, query string, limit int) ([]SearchHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return nil, err
	}
	if sum == nil {
		return nil, ErrChatNotFound
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		lines, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "events.jsonl"))
		if err != nil {
			return nil, err
		}
	}

	hits := make([]SearchHit, 0, limit)
	appendHit := func(hit SearchHit) {
		if strings.TrimSpace(hit.Snippet) == "" {
			return
		}
		hits = append(hits, hit)
	}

	for _, line := range lines {
		runID, _ := line["runId"].(string)
		ts := int64FromAny(line["updatedAt"])
		lineType, _ := line["_type"].(string)
		if lineType == "" {
			lineType, _ = line["type"].(string)
		}
		switch lineType {
		case "query":
			payload, _ := line["query"].(map[string]any)
			message := stringValue(payload["message"])
			if score := sessionSearchScore(message, needle); score > 0 {
				appendHit(SearchHit{
					Kind:      "query",
					ChatID:    chatID,
					RunID:     runID,
					Role:      defaultSearchRole(stringValue(payload["role"])),
					Timestamp: ts,
					Snippet:   buildSnippet(message, needle),
					Score:     score,
				})
			}
		case "react", "plan-execute", "step":
			stage := stringValue(line["stage"])
			messages, _ := line["messages"].([]any)
			for _, raw := range messages {
				msg, _ := raw.(map[string]any)
				if msg == nil {
					continue
				}
				role := stringValue(msg["role"])
				text := searchMessageText(msg)
				if score := sessionSearchScore(text, needle); score > 0 {
					hitTimestamp := int64FromAny(msg["ts"])
					if hitTimestamp == 0 {
						hitTimestamp = ts
					}
					appendHit(SearchHit{
						Kind:      "message",
						ChatID:    chatID,
						RunID:     runID,
						Stage:     stage,
						Role:      role,
						Timestamp: hitTimestamp,
						Snippet:   buildSnippet(text, needle),
						Score:     score,
						Meta: map[string]any{
							"taskId": stringValue(line["taskId"]),
						},
					})
				}
			}
			if approval, ok := line["approval"].(map[string]any); ok {
				summary := stringValue(approval["summary"])
				if score := sessionSearchScore(summary, needle); score > 0 {
					appendHit(SearchHit{
						Kind:      "approval",
						ChatID:    chatID,
						RunID:     runID,
						Stage:     stage,
						Role:      "user",
						Timestamp: ts,
						Snippet:   buildSnippet(summary, needle),
						Score:     score,
					})
				}
			}
		case "event", "steer":
			event, _ := line["event"].(map[string]any)
			text := searchEventText(event)
			if score := sessionSearchScore(text, needle); score > 0 {
				appendHit(SearchHit{
					Kind:      "event",
					ChatID:    chatID,
					RunID:     runID,
					Timestamp: ts,
					Snippet:   buildSnippet(text, needle),
					Score:     score,
					Meta: map[string]any{
						"type": stringValue(event["type"]),
					},
				})
			}
		case "submit":
			text := searchEventText(line["submit"]) + "\n" + searchEventText(line["answer"])
			if score := sessionSearchScore(text, needle); score > 0 {
				appendHit(SearchHit{
					Kind:      "submit",
					ChatID:    chatID,
					RunID:     runID,
					Timestamp: ts,
					Snippet:   buildSnippet(text, needle),
					Score:     score,
				})
			}
		default:
			text := searchEventText(line)
			if score := sessionSearchScore(text, needle); score > 0 {
				appendHit(SearchHit{
					Kind:      "event",
					ChatID:    chatID,
					RunID:     runID,
					Timestamp: ts,
					Snippet:   buildSnippet(text, needle),
					Score:     score,
				})
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Timestamp > hits[j].Timestamp
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func searchMessageText(msg map[string]any) string {
	parts := []string{
		extractTextFromContent(msg["content"]),
		extractTextFromContent(msg["reasoning_content"]),
		stringValue(msg["name"]),
		stringValue(msg["tool_call_id"]),
	}
	if toolCalls, ok := msg["tool_calls"].([]any); ok {
		for _, raw := range toolCalls {
			call, _ := raw.(map[string]any)
			if call == nil {
				continue
			}
			function, _ := call["function"].(map[string]any)
			parts = append(parts, stringValue(call["id"]), stringValue(function["name"]), stringValue(function["arguments"]))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func searchEventText(raw any) string {
	if raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		data, _ := json.Marshal(value)
		return strings.TrimSpace(string(data))
	case []any:
		data, _ := json.Marshal(value)
		return strings.TrimSpace(string(data))
	default:
		data, _ := json.Marshal(value)
		return strings.TrimSpace(string(data))
	}
}

func sessionSearchScore(text string, needle string) int {
	source := strings.ToLower(strings.TrimSpace(text))
	if source == "" || needle == "" {
		return 0
	}
	count := strings.Count(source, needle)
	if count == 0 {
		return 0
	}
	return count*100 + minInt(len(source), 200)
}

func buildSnippet(text string, needle string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	index := strings.Index(lower, needle)
	if index < 0 || len(text) <= 240 {
		return text
	}
	start := index - 80
	if start < 0 {
		start = 0
	}
	end := index + len(needle) + 120
	if end > len(text) {
		end = len(text)
	}
	snippet := strings.TrimSpace(text[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet += "..."
	}
	return snippet
}

func defaultSearchRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "user"
	}
	return role
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
