package tools

import (
	"context"
	"strings"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/catalog"
	. "agent-platform/internal/contracts"
)

func (t *RuntimeToolExecutor) requireKBaseContext(toolName string, execCtx *ExecutionContext) (string, *ToolExecutionResult) {
	if t.kbase == nil {
		return "", &ToolExecutionResult{Output: "kbase manager not configured", Error: "kbase_not_configured", ExitCode: -1}
	}
	if execCtx == nil || strings.TrimSpace(execCtx.Session.AgentKey) == "" {
		return "", &ToolExecutionResult{Output: toolName + " requires an active agent execution context", Error: "kbase_context_required", ExitCode: -1}
	}
	if !strings.EqualFold(strings.TrimSpace(execCtx.Session.Mode), catalog.AgentModeKBase) {
		return "", &ToolExecutionResult{Output: toolName + " is only supported for mode: KBASE", Error: "kbase_unsupported_agent_mode", ExitCode: -1}
	}
	return strings.TrimSpace(execCtx.Session.AgentKey), nil
}

func (t *RuntimeToolExecutor) invokeKBaseSearch(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	agentKey, contextErr := t.requireKBaseContext(toolName, execCtx)
	if contextErr != nil {
		return *contextErr, nil
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return ToolExecutionResult{Output: "query must not be blank", Error: "missing_query", ExitCode: -1}, nil
	}
	limit := int(int64Arg(args, "limit"))
	result, err := t.kbase.Search(ctx, agentKey, query, kbase.SearchOptions{Limit: limit})
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{
		"agentKey": result.AgentKey,
		"query":    result.Query,
		"count":    result.Count,
		"results":  result.Results,
		"stale":    result.Stale,
		"indexing": result.Indexing,
	}), nil
}

func (t *RuntimeToolExecutor) invokeKBaseRead(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	agentKey, contextErr := t.requireKBaseContext(toolName, execCtx)
	if contextErr != nil {
		return *contextErr, nil
	}
	result, err := t.kbase.Read(agentKey, kbase.ReadOptions{
		ChunkID: strings.TrimSpace(stringArg(args, "chunkId")),
		Path:    strings.TrimSpace(stringArg(args, "path")),
		Offset:  int(int64Arg(args, "offset")),
		Limit:   int(int64Arg(args, "limit")),
	})
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{
		"found":      result.Found,
		"chunkId":    result.ChunkID,
		"path":       result.Path,
		"heading":    result.Heading,
		"startLine":  result.StartLine,
		"endLine":    result.EndLine,
		"pageStart":  result.PageStart,
		"pageEnd":    result.PageEnd,
		"slideStart": result.SlideStart,
		"slideEnd":   result.SlideEnd,
		"sourceType": result.SourceType,
		"content":    result.Content,
	}), nil
}

func (t *RuntimeToolExecutor) invokeKBaseStatus(toolName string, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	agentKey, contextErr := t.requireKBaseContext(toolName, execCtx)
	if contextErr != nil {
		return *contextErr, nil
	}
	status, err := t.kbase.Status(agentKey)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{
		"agentKey":        status.AgentKey,
		"mode":            status.Mode,
		"storageLocation": status.StorageLocation,
		"storageDir":      status.StorageDir,
		"workspaceRoot":   status.WorkspaceRoot,
		"indexing":        status.Indexing,
		"stale":           status.Stale,
		"lastIndexedAt":   status.LastIndexedAt,
		"files":           status.Files,
		"chunks":          status.Chunks,
		"embedding":       status.Embedding,
		"lastRun":         status.LastRun,
		"fileStats":       status.FileStats,
	}), nil
}

func (t *RuntimeToolExecutor) invokeKBaseRefresh(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	agentKey, contextErr := t.requireKBaseContext(toolName, execCtx)
	if contextErr != nil {
		return *contextErr, nil
	}
	result, err := t.kbase.Refresh(ctx, agentKey, kbase.RefreshOptions{
		Force: boolArg(args, "force"),
		Mode:  "tool",
	})
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{
		"agentKey":      result.AgentKey,
		"mode":          result.Mode,
		"status":        result.Status,
		"scannedFiles":  result.ScannedFiles,
		"changedFiles":  result.ChangedFiles,
		"deletedFiles":  result.DeletedFiles,
		"indexedChunks": result.IndexedChunks,
		"error":         result.Error,
	}), nil
}
