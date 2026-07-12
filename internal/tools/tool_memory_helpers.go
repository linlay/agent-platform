package tools

import (
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/memory"
)

func requireMemoryToolContext(execCtx *ExecutionContext, toolName string) (string, string, string, *ToolExecutionResult) {
	if execCtx == nil || strings.TrimSpace(execCtx.Session.AgentKey) == "" {
		return "", "", "", &ToolExecutionResult{
			Output:   toolName + " requires an active agent execution context",
			Error:    "memory_context_required",
			ExitCode: -1,
		}
	}
	requestID := strings.TrimSpace(execCtx.Request.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(execCtx.Session.RequestID)
	}
	chatID := strings.TrimSpace(execCtx.Request.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(execCtx.Session.ChatID)
	}
	return strings.TrimSpace(execCtx.Session.AgentKey), requestID, chatID, nil
}

func memoryToolLimit(limit int, fallback int) int {
	return clampMemoryLimit(limit, fallback)
}

func clampMemoryLimit(limit int, fallback int) int {
	if fallback <= 0 {
		fallback = 10
	}
	if limit <= 0 {
		limit = fallback
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizeMemoryCategory(category string) string {
	return memory.NormalizeCategory(category)
}

func normalizeMemorySourceType(sourceType string) string {
	if strings.TrimSpace(sourceType) == "" {
		return "tool-write"
	}
	return strings.ToLower(strings.TrimSpace(sourceType))
}

func normalizeMemoryImportance(importance int) int {
	if importance <= 0 {
		importance = 5
	}
	if importance < 1 {
		return 1
	}
	if importance > 10 {
		return 10
	}
	return importance
}

func normalizeMemorySubjectKey(subjectKey string, chatID string, agentKey string) string {
	if strings.TrimSpace(subjectKey) != "" {
		return strings.TrimSpace(subjectKey)
	}
	if strings.TrimSpace(chatID) != "" {
		return "chat:" + strings.TrimSpace(chatID)
	}
	if strings.TrimSpace(agentKey) != "" {
		return "agent:" + strings.TrimSpace(agentKey)
	}
	return "_global"
}

func normalizeMemoryTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func stringListArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return []string{}
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func memoryToolRecordValue(record memory.ToolRecord) map[string]any {
	value := map[string]any{
		"id":             record.ID,
		"agentKey":       record.AgentKey,
		"subjectKey":     record.SubjectKey,
		"kind":           record.Kind,
		"refId":          record.RefID,
		"scopeType":      record.ScopeType,
		"scopeKey":       record.ScopeKey,
		"title":          record.Title,
		"content":        record.Content,
		"sourceType":     record.SourceType,
		"category":       record.Category,
		"importance":     record.Importance,
		"confidence":     record.Confidence,
		"status":         record.Status,
		"tags":           append([]string(nil), record.Tags...),
		"hasEmbedding":   record.HasEmbedding,
		"embeddingModel": record.EmbeddingModel,
		"createdAt":      record.CreatedAt,
		"updatedAt":      record.UpdatedAt,
		"accessCount":    record.AccessCount,
	}
	// lastAccessedAt is optional. A nil pointer must stay absent on the
	// structured tool/stream payload instead of becoming JSON null.
	if record.LastAccessedAt != nil {
		value["lastAccessedAt"] = *record.LastAccessedAt
	}
	return value
}
