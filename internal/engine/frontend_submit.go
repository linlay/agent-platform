package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

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
	toolName := execCtx.CurrentToolName
	toolID := execCtx.CurrentToolID
	timeout := normalizeBudget(execCtx.Budget).Tool.Timeout()
	execCtx.RunLoopState = RunLoopStateWaitingSubmit
	execCtx.RunControl.TransitionState(RunLoopStateWaitingSubmit)
	waitStarted := time.Now()

	result, err := execCtx.RunControl.AwaitSubmitWithTimeout(ctx, toolID, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			elapsedMs := time.Since(waitStarted).Milliseconds()
			timeoutMs := timeout.Milliseconds()
			payload := NewErrorPayload(
				"frontend_submit_timeout",
				resolveFrontendTimeoutMessage(toolName, toolID, timeoutMs, elapsedMs),
				ErrorScopeFrontendSubmit,
				ErrorCategoryTimeout,
				map[string]any{
					"toolId":    toolID,
					"toolName":  toolName,
					"timeoutMs": timeoutMs,
					"elapsedMs": elapsedMs,
				},
			)
			return ToolExecutionResult{
				Output:     marshalJSON(payload),
				Structured: payload,
				Error:      "frontend_submit_timeout",
				ExitCode:   -1,
			}, nil
		}
		return ToolExecutionResult{}, err
	}
	execCtx.RunLoopState = RunLoopStateToolExecuting
	execCtx.RunControl.TransitionState(RunLoopStateToolExecuting)

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

func resolveFrontendTimeoutMessage(toolName string, toolID string, timeoutMs int64, elapsedMs int64) string {
	if toolName == "" {
		toolName = "unknown"
	}
	if toolID == "" {
		toolID = "unknown"
	}
	return "Frontend tool submit timeout: tool=" + toolName + ", toolId=" + toolID + ", elapsedMs=" + formatInt64(elapsedMs) + ", timeoutMs=" + formatInt64(timeoutMs)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func marshalJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
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
