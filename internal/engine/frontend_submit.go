package engine

import (
	"context"
	"encoding/json"

	"agent-platform-runner-go/internal/api"
)

type FrontendSubmitCoordinator struct{}

func NewFrontendSubmitCoordinator() *FrontendSubmitCoordinator {
	return &FrontendSubmitCoordinator{}
}

func (c *FrontendSubmitCoordinator) Await(ctx context.Context, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	_ = c
	if execCtx == nil || execCtx.RunControl == nil {
		return ToolExecutionResult{}, ErrRunControlUnavailable
	}

	result, err := execCtx.RunControl.AwaitSubmit(ctx, execCtx.CurrentToolID)
	if err != nil {
		return ToolExecutionResult{}, err
	}

	payload := map[string]any{
		"status": "submitted",
		"runId":  result.Request.RunID,
		"toolId": result.Request.ToolID,
		"params": result.Request.Params,
	}
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   0,
	}, nil
}

func NewFrontendSubmitRequest(session QuerySession, toolID string, payload any, viewID string) DeltaRequestSubmit {
	return DeltaRequestSubmit{
		RequestID: session.RequestID,
		ChatID:    session.ChatID,
		RunID:     session.RunID,
		ToolID:    toolID,
		Payload:   payload,
		ViewID:    viewID,
	}
}

func NewSteerDelta(req api.SteerRequest) DeltaRequestSteer {
	return DeltaRequestSteer{
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
		RunID:     req.RunID,
		SteerID:   req.SteerID,
		Message:   req.Message,
	}
}
