package tools

import (
	"strings"

	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/observability"
)

func logMemoryToolInvocation(toolName string, status string, execCtx *ExecutionContext, fields map[string]any) {
	payload := map[string]any{
		"source":   "tool",
		"toolName": strings.TrimSpace(toolName),
		"status":   strings.TrimSpace(status),
	}
	if execCtx != nil {
		payload["requestId"] = strings.TrimSpace(execCtx.Session.RequestID)
		payload["runId"] = strings.TrimSpace(execCtx.Session.RunID)
		payload["chatId"] = strings.TrimSpace(execCtx.Session.ChatID)
		payload["agentKey"] = strings.TrimSpace(execCtx.Session.AgentKey)
		payload["teamId"] = strings.TrimSpace(execCtx.Session.TeamID)
		payload["userKey"] = strings.TrimSpace(execCtx.Session.Subject)
	}
	for key, value := range fields {
		payload[key] = value
	}
	observability.LogMemoryOperation("tool_invocation", payload)
}
