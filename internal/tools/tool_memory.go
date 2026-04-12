package tools

import (
	"fmt"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
)

func (t *RuntimeToolExecutor) invokeMemorySearch(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		return *contextErr, nil
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return ToolExecutionResult{Output: "query must not be blank", Error: "missing_query", ExitCode: -1}, nil
	}
	items, err := t.memory.SearchDetailed(agentKey, query, stringArg(args, "category"), memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit))
	if err != nil {
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
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeMemoryRead(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		return *contextErr, nil
	}
	id := stringArg(args, "id")
	if id != "" {
		item, err := t.memory.ReadDetail(agentKey, id)
		if err != nil {
			return ToolExecutionResult{}, err
		}
		if item == nil {
			return structuredResult(map[string]any{"found": false}), nil
		}
		return structuredResult(map[string]any{"found": true, "memory": memoryToolRecordValue(*item)}), nil
	}
	items, err := t.memory.List(agentKey, stringArg(args, "category"), memoryToolLimit(int(int64Arg(args, "limit")), t.cfg.Memory.SearchDefaultLimit), stringArg(args, "sort"))
	if err != nil {
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		results = append(results, memoryToolRecordValue(item))
	}
	return structuredResult(map[string]any{"count": len(results), "results": results}), nil
}

func (t *RuntimeToolExecutor) invokeMemoryWrite(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, requestID, chatID, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		return *contextErr, nil
	}
	content := strings.TrimSpace(stringArg(args, "content"))
	if content == "" {
		return ToolExecutionResult{Output: "content must not be blank", Error: "missing_content", ExitCode: -1}, nil
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         fmt.Sprintf("mem_%d", time.Now().UnixNano()),
		RequestID:  requestID,
		ChatID:     chatID,
		AgentKey:   agentKey,
		SubjectKey: normalizeMemorySubjectKey("", chatID, agentKey),
		Summary:    content,
		SourceType: normalizeMemorySourceType("tool-write"),
		Category:   normalizeMemoryCategory(stringArg(args, "category")),
		Importance: normalizeMemoryImportance(int(int64Arg(args, "importance"))),
		Tags:       normalizeMemoryTags(stringListArg(args, "tags")),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := t.memory.Write(item); err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{
		"id":           item.ID,
		"status":       "stored",
		"subjectKey":   item.SubjectKey,
		"sourceType":   item.SourceType,
		"category":     item.Category,
		"importance":   item.Importance,
		"hasEmbedding": false,
	}), nil
}
