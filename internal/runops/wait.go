package runops

import (
	"agent-platform/internal/contracts"
	"context"
	"fmt"
)

func (h *ToolHandler) CheckWaitCondition(_ context.Context, c contracts.WaitCondition, execCtx *contracts.ExecutionContext) (bool, string, error) {
	origin, failure := h.callerOrigin(execCtx)
	if failure != nil {
		return false, "", fmt.Errorf("%s: %s", failure.Error, failure.Output)
	}
	if c.RunID == execCtx.Session.RunID {
		return false, "", fmt.Errorf("cannot wait for the current Run")
	}
	if failure = h.requireOwnedRun(c.RunID, origin); failure != nil {
		return false, "", fmt.Errorf("%s: %s", failure.Error, failure.Output)
	}
	snapshot, err := h.service.GetRunStatus(c.RunID)
	if err != nil {
		return false, "", err
	}
	return snapshot.CompletedAt > 0 && (snapshot.Status == "completed" || snapshot.Status == "failed" || snapshot.Status == "interrupted"), snapshot.Status, nil
}
