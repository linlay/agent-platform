package tools

import (
	"fmt"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/memory"
)

func (t *RuntimeToolExecutor) invokeMemorySearch(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": "missing_query"})
		return ToolExecutionResult{Output: "query must not be blank", Error: "missing_query", ExitCode: -1}, nil
	}
	category := stringArg(args, "category")
	limit := memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit)
	items, err := t.memory.SearchDetailed(agentKey, query, category, limit)
	if err != nil {
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"query": query, "category": category, "limit": limit, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, map[string]any{
			"memory":    memoryToolRecordValue(item.Memory),
			"score":     item.Score,
			"matchType": item.MatchType,
		})
	}
	payload := map[string]any{"results": results, "count": len(results)}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"query": query, "category": category, "limit": limit, "count": len(results)})
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeMemoryRead(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	id := stringArg(args, "id")
	if id != "" {
		item, err := t.memory.ReadDetail(agentKey, id)
		if err != nil {
			logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"id": id, "error": err.Error()})
			return ToolExecutionResult{}, err
		}
		if item == nil {
			logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "found": false})
			return structuredResult(map[string]any{"found": false}), nil
		}
		logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "found": true})
		return structuredResult(map[string]any{"found": true, "memory": memoryToolRecordValue(*item)}), nil
	}
	category := stringArg(args, "category")
	limit := memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit)
	sortBy := stringArg(args, "sort")
	items, err := t.memory.List(agentKey, category, limit, sortBy)
	if err != nil {
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"category": category, "limit": limit, "sort": sortBy, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, memoryToolRecordValue(item))
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"category": category, "limit": limit, "sort": sortBy, "count": len(results)})
	return structuredResult(map[string]any{"count": len(results), "results": results}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryWrite(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, requestID, chatID, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	content := strings.TrimSpace(stringArg(args, "content"))
	if content == "" {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": "missing_content"})
		return ToolExecutionResult{Output: "content must not be blank", Error: "missing_content", ExitCode: -1}, nil
	}
	now := time.Now().UnixMilli()
	scopeType := stringArg(args, "scopeType")
	scopeKey := strings.TrimSpace(stringArg(args, "scopeKey"))
	title := strings.TrimSpace(stringArg(args, "title"))
	confidence := floatArg(args, "confidence")
	if strings.TrimSpace(scopeType) == "" {
		scopeType = memory.ScopeAgent
	}
	if scopeKey == "" {
		scopeKey = defaultScopeKeyForTool(scopeType, execCtx)
	}
	item := api.StoredMemoryResponse{
		ID:         fmt.Sprintf("mem_%d", time.Now().UnixNano()),
		RequestID:  requestID,
		ChatID:     chatID,
		AgentKey:   agentKey,
		Kind:       memory.KindFact,
		RefID:      chatID,
		ScopeType:  scopeType,
		ScopeKey:   scopeKey,
		Title:      title,
		SubjectKey: normalizeMemorySubjectKey("", chatID, agentKey),
		Summary:    content,
		SourceType: normalizeMemorySourceType("tool-write"),
		Category:   normalizeMemoryCategory(stringArg(args, "category")),
		Importance: normalizeMemoryImportance(int(int64Arg(args, "importance"))),
		Confidence: normalizeMemoryConfidenceArg(confidence, memory.KindFact),
		Status:     memory.StatusActive,
		Tags:       normalizeMemoryTags(stringListArg(args, "tags")),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := t.memory.Write(item); err != nil {
		if memory.IsMemorySafetyError(err) {
			logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"id": item.ID, "scopeType": item.ScopeType, "scopeKey": item.ScopeKey, "category": item.Category, "reason": err.Error()})
			return ToolExecutionResult{Output: err.Error(), Error: "memory_write_rejected", ExitCode: -1}, nil
		}
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"id": item.ID, "scopeType": item.ScopeType, "scopeKey": item.ScopeKey, "category": item.Category, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": item.ID, "scopeType": item.ScopeType, "scopeKey": item.ScopeKey, "category": item.Category, "requestId": requestID, "chatId": chatID})
	return structuredResult(map[string]any{
		"status": "stored",
		"memory": memoryToolRecordValue(memory.ToolRecord{
			ID:         item.ID,
			AgentKey:   item.AgentKey,
			SubjectKey: item.SubjectKey,
			Kind:       item.Kind,
			RefID:      item.RefID,
			ScopeType:  item.ScopeType,
			ScopeKey:   item.ScopeKey,
			Title:      item.Title,
			Content:    item.Summary,
			SourceType: item.SourceType,
			Category:   item.Category,
			Importance: item.Importance,
			Confidence: item.Confidence,
			Status:     item.Status,
			Tags:       item.Tags,
			CreatedAt:  item.CreatedAt,
			UpdatedAt:  item.UpdatedAt,
		}),
	}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryUpdate(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	mutator, ok := t.memory.(memory.Mutator)
	if !ok {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_mutator_not_configured"})
		return ToolExecutionResult{Output: "memory mutator not configured", Error: "memory_mutator_not_configured", ExitCode: -1}, nil
	}
	id := strings.TrimSpace(stringArg(args, "id"))
	if id == "" {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": "missing_id"})
		return ToolExecutionResult{Output: "id must not be blank", Error: "missing_id", ExitCode: -1}, nil
	}
	input := memory.MutationInput{ID: id}
	if value := strings.TrimSpace(stringArg(args, "title")); value != "" {
		input.Title = &value
	}
	if value := strings.TrimSpace(stringArg(args, "content")); value != "" {
		input.Summary = &value
	}
	if value := strings.TrimSpace(stringArg(args, "category")); value != "" {
		input.Category = &value
	}
	if value := strings.TrimSpace(stringArg(args, "scopeType")); value != "" {
		input.ScopeType = &value
	}
	if value := strings.TrimSpace(stringArg(args, "scopeKey")); value != "" {
		input.ScopeKey = &value
	}
	if value := strings.TrimSpace(stringArg(args, "status")); value != "" {
		input.Status = &value
	}
	if raw, ok := args["importance"]; ok && raw != nil {
		value := int(int64Arg(args, "importance"))
		input.Importance = &value
	}
	if raw, ok := args["confidence"]; ok && raw != nil {
		value := floatArg(args, "confidence")
		input.Confidence = &value
	}
	if _, ok := args["tags"]; ok {
		input.Tags = normalizeMemoryTags(stringListArg(args, "tags"))
		input.ReplaceTags = true
	}
	record, err := mutator.Update(agentKey, input)
	if err != nil {
		if memory.IsMemorySafetyError(err) {
			logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"id": id, "reason": err.Error()})
			return ToolExecutionResult{Output: err.Error(), Error: "memory_write_rejected", ExitCode: -1}, nil
		}
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"id": id, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	if record == nil {
		logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "updated": false})
		return structuredResult(map[string]any{"updated": false}), nil
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "updated": true, "status": record.Status})
	return structuredResult(map[string]any{"updated": true, "memory": memoryToolRecordValue(*record)}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryForget(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	mutator, ok := t.memory.(memory.Mutator)
	if !ok {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_mutator_not_configured"})
		return ToolExecutionResult{Output: "memory mutator not configured", Error: "memory_mutator_not_configured", ExitCode: -1}, nil
	}
	id := stringArg(args, "id")
	status := stringArg(args, "status")
	record, err := mutator.Forget(agentKey, id, status)
	if err != nil {
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"id": id, "status": status, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	if record == nil {
		logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "updated": false})
		return structuredResult(map[string]any{"updated": false}), nil
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "updated": true, "status": record.Status})
	return structuredResult(map[string]any{"updated": true, "memory": memoryToolRecordValue(*record)}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryTimeline(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	provider, ok := t.memory.(memory.TimelineProvider)
	if !ok {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_timeline_not_configured"})
		return ToolExecutionResult{Output: "memory timeline provider not configured", Error: "memory_timeline_not_configured", ExitCode: -1}, nil
	}
	id := stringArg(args, "id")
	limit := memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit)
	items, err := provider.Timeline(agentKey, id, limit)
	if err != nil {
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"id": id, "limit": limit, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, map[string]any{
			"memory":       memoryToolRecordValue(item.Memory),
			"relationType": item.RelationType,
			"direction":    item.Direction,
		})
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"id": id, "limit": limit, "count": len(results)})
	return structuredResult(map[string]any{"count": len(results), "results": results}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryPromote(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	promoter, ok := t.memory.(memory.Promoter)
	if !ok {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_promoter_not_configured"})
		return ToolExecutionResult{Output: "memory promoter not configured", Error: "memory_promoter_not_configured", ExitCode: -1}, nil
	}
	scopeType := stringArg(args, "scopeType")
	if strings.TrimSpace(scopeType) == "" {
		scopeType = memory.ScopeAgent
	}
	sourceID := firstNonBlank(stringArg(args, "id"), stringArg(args, "sourceId"))
	record, err := promoter.Promote(agentKey, memory.PromoteInput{
		SourceID:      sourceID,
		Title:         stringArg(args, "title"),
		Summary:       stringArg(args, "content"),
		Category:      stringArg(args, "category"),
		ScopeType:     scopeType,
		ScopeKey:      firstNonBlank(stringArg(args, "scopeKey"), defaultScopeKeyForTool(scopeType, execCtx)),
		Importance:    int(int64Arg(args, "importance")),
		Confidence:    floatArg(args, "confidence"),
		Tags:          normalizeMemoryTags(stringListArg(args, "tags")),
		ArchiveSource: boolArg(args, "archiveSource"),
	})
	if err != nil {
		if memory.IsMemorySafetyError(err) {
			logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"sourceId": sourceID, "scopeType": scopeType, "reason": err.Error()})
			return ToolExecutionResult{Output: err.Error(), Error: "memory_write_rejected", ExitCode: -1}, nil
		}
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"sourceId": sourceID, "scopeType": scopeType, "error": err.Error()})
		return ToolExecutionResult{}, err
	}
	if record == nil {
		logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"sourceId": sourceID, "promoted": false})
		return structuredResult(map[string]any{"promoted": false}), nil
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{"sourceId": sourceID, "promoted": true, "id": record.ID, "scopeType": record.ScopeType})
	return structuredResult(map[string]any{"promoted": true, "memory": memoryToolRecordValue(*record)}), nil
}

func floatArg(args map[string]any, key string) float64 {
	switch value := args[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string) bool {
	value, ok := args[key].(bool)
	return ok && value
}

func normalizeMemoryConfidenceArg(value float64, kind string) float64 {
	if value <= 0 {
		if kind == memory.KindObservation {
			return 0.7
		}
		return 0.9
	}
	if value > 1 {
		return 1
	}
	return value
}

func defaultScopeKeyForTool(scopeType string, execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(scopeType)) {
	case memory.ScopeUser:
		if strings.TrimSpace(execCtx.Session.Subject) == "" {
			return "user:_local_default"
		}
		return "user:" + strings.TrimSpace(execCtx.Session.Subject)
	case memory.ScopeTeam:
		if strings.TrimSpace(execCtx.Session.TeamID) == "" {
			return "team:default"
		}
		return "team:" + strings.TrimSpace(execCtx.Session.TeamID)
	case memory.ScopeGlobal:
		return "global:default"
	case memory.ScopeChat:
		if strings.TrimSpace(execCtx.Session.ChatID) == "" {
			return "chat:unknown"
		}
		return "chat:" + strings.TrimSpace(execCtx.Session.ChatID)
	default:
		if strings.TrimSpace(execCtx.Session.AgentKey) == "" {
			return "agent:default"
		}
		return "agent:" + strings.TrimSpace(execCtx.Session.AgentKey)
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
