package memory

import (
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/observability"
)

func logMemoryOperation(operation string, fields map[string]any) {
	observability.LogMemoryOperation(operation, fields)
}

func logMemoryRead(operation string, agentKey string, id string, found bool) {
	logMemoryOperation(operation, map[string]any{
		"agentKey": strings.TrimSpace(agentKey),
		"id":       strings.TrimSpace(id),
		"found":    found,
	})
}

func logMemoryWrite(operation string, item api.StoredMemoryResponse) {
	logMemoryOperation(operation, map[string]any{
		"id":         item.ID,
		"agentKey":   item.AgentKey,
		"chatId":     item.ChatID,
		"kind":       item.Kind,
		"scopeType":  item.ScopeType,
		"scopeKey":   item.ScopeKey,
		"category":   item.Category,
		"importance": item.Importance,
		"status":     item.Status,
		"tags":       item.Tags,
	})
}

func logMemoryWriteRejected(operation string, item api.StoredMemoryResponse, err error) {
	logMemoryOperation(operation, map[string]any{
		"id":         item.ID,
		"agentKey":   item.AgentKey,
		"chatId":     item.ChatID,
		"requestId":  item.RequestID,
		"kind":       item.Kind,
		"scopeType":  item.ScopeType,
		"scopeKey":   item.ScopeKey,
		"category":   item.Category,
		"status":     item.Status,
		"source":     item.SourceType,
		"sourceType": item.SourceType,
		"reason":     strings.TrimSpace(err.Error()),
	})
}
