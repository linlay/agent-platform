package memoryworker

import (
	"context"
	"encoding/json"
	"slices"

	"agent-platform/internal/contracts"
)

type ToolHandler struct{ Worker *Worker }

func (*ToolHandler) ToolNames() []string { return []string{"memory_update"} }
func (h *ToolHandler) Invoke(_ context.Context, _ string, args map[string]any, ec *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	fail := func(code string) (contracts.ToolExecutionResult, error) {
		return contracts.ToolExecutionResult{Error: code, Output: code, ExitCode: -1}, nil
	}
	if len(args) != 0 {
		return fail("memory_update_invalid_params")
	}
	if ec == nil || !ec.Session.AgentHasMemoryConfig || !contracts.OrdinaryNativeRoot(ec.Session) || ec.Session.RunOrigin != nil || !slices.Contains(ec.Session.ToolNames, "memory_update") || contracts.IsReadOnlyToolExecutionPolicy(ec.Session.ToolExecutionPolicy) || contracts.IsReadOnlyToolExecutionPolicy(ec.ToolExecutionPolicy) {
		return fail("memory_update_not_allowed")
	}
	if h.Worker == nil {
		return fail("memory_update_unavailable")
	}
	status, err := h.Worker.Trigger()
	if err != nil {
		return fail("memory_update_unavailable")
	}
	data := map[string]any{"accepted": true, "status": status, "message": "Memory maintenance queued or already running. Only completed chats are eligible; this is not confirmation that any fact has been saved."}
	b, _ := json.Marshal(data)
	return contracts.ToolExecutionResult{Output: string(b), Structured: data}, nil
}
