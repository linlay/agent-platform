package tools

import (
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/memory"
)

func (t *RuntimeToolExecutor) invokeMemoryConsolidate(toolName string, _ map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.memory == nil {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_not_configured"})
		return ToolExecutionResult{Output: "memory store not configured", Error: "memory_not_configured", ExitCode: -1}, nil
	}
	agentKey, _, _, contextErr := requireMemoryToolContext(execCtx, toolName)
	if contextErr != nil {
		logMemoryToolInvocation(toolName, "rejected", execCtx, map[string]any{"reason": contextErr.Error})
		return *contextErr, nil
	}
	consolidator, ok := t.memory.(memory.Consolidator)
	if !ok {
		logMemoryToolInvocation(toolName, "skipped", execCtx, map[string]any{"reason": "memory_consolidator_not_configured"})
		return ToolExecutionResult{Output: "memory consolidator not configured", Error: "memory_consolidator_not_configured", ExitCode: -1}, nil
	}
	result, err := consolidator.Consolidate(agentKey)
	if err != nil {
		logMemoryToolInvocation(toolName, "error", execCtx, map[string]any{"error": err.Error()})
		return ToolExecutionResult{}, err
	}
	logMemoryToolInvocation(toolName, "ok", execCtx, map[string]any{
		"archivedCount": result.ArchivedCount,
		"mergedCount":   result.MergedCount,
		"promotedCount": result.PromotedCount,
	})
	return structuredResult(map[string]any{
		"archivedCount": result.ArchivedCount,
		"mergedCount":   result.MergedCount,
		"promotedCount": result.PromotedCount,
	}), nil
}
