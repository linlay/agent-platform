package memory

import (
	"bytes"
	"encoding/json"
	"strings"

	"agent-platform/internal/api"
)

const (
	HistoryStatusOK = "ok"
)

type HistoryEvent struct {
	ID         string         `json:"id"`
	Timestamp  int64          `json:"ts"`
	AgentKey   string         `json:"agentKey,omitempty"`
	ChatID     string         `json:"chatId,omitempty"`
	RunID      string         `json:"runId,omitempty"`
	RequestID  string         `json:"requestId,omitempty"`
	UserKey    string         `json:"userKey,omitempty"`
	MemoryID   string         `json:"memoryId,omitempty"`
	MemoryKind string         `json:"memoryKind,omitempty"`
	ScopeType  string         `json:"scopeType,omitempty"`
	ScopeKey   string         `json:"scopeKey,omitempty"`
	Operation  string         `json:"operation"`
	Source     string         `json:"source,omitempty"`
	Status     string         `json:"status"`
	Before     map[string]any `json:"before,omitempty"`
	After      map[string]any `json:"after,omitempty"`
	Delta      map[string]any `json:"delta,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type HistoryFilter struct {
	AgentKey  string
	ChatID    string
	RunID     string
	MemoryID  string
	Operation string
	Limit     int
	Cursor    string
}

type HistoryResult struct {
	Events     []HistoryEvent
	NextCursor string
}

type HistoryProvider interface {
	History(filter HistoryFilter) (HistoryResult, error)
}

func normalizeHistoryLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func historyJSON(value map[string]any) string {
	if len(value) == 0 {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func decodeHistoryJSON(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var out map[string]any
	// History payloads can carry diagnostic/source maps. Preserve numeric JSON
	// tokens while rehydrating them: json.Unmarshal would turn a valid epoch-ms
	// integer into float64, then the public audit response would reject its own
	// otherwise valid persisted record.
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func historyAfterFromStored(item api.StoredMemoryResponse) map[string]any {
	return map[string]any{
		"id":         item.ID,
		"kind":       item.Kind,
		"title":      item.Title,
		"summary":    item.Summary,
		"category":   item.Category,
		"importance": item.Importance,
		"confidence": item.Confidence,
		"status":     item.Status,
		"scopeType":  item.ScopeType,
		"scopeKey":   item.ScopeKey,
	}
}
