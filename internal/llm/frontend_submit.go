package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
)

type FrontendSubmitCoordinator struct {
	frontend *frontendtools.Registry
}

func NewFrontendSubmitCoordinator(frontend *frontendtools.Registry) *FrontendSubmitCoordinator {
	return &FrontendSubmitCoordinator{frontend: frontend}
}

func (c *FrontendSubmitCoordinator) Await(ctx context.Context, execCtx *ExecutionContext, args map[string]any) (ToolExecutionResult, error) {
	if execCtx == nil || execCtx.RunControl == nil {
		return ToolExecutionResult{}, ErrRunControlUnavailable
	}
	toolName := execCtx.CurrentToolName
	awaitingID := execCtx.CurrentToolID
	var (
		handler frontendtools.Handler
		ok      bool
	)
	if c.frontend != nil {
		handler, ok = c.frontend.Handler(toolName)
	}
	if !ok {
		execCtx.RunControl.ClearExpectedSubmit(awaitingID)
		payload := NewErrorPayload(
			"frontend_tool_handler_not_registered",
			"frontend tool handler not registered: "+toolName,
			ErrorScopeFrontendSubmit,
			ErrorCategoryTool,
			map[string]any{
				"awaitingId": awaitingID,
				"toolName":   toolName,
			},
		)
		return ToolExecutionResult{
			Output:     marshalJSON(payload),
			Structured: payload,
			Error:      "frontend_tool_handler_not_registered",
			ExitCode:   -1,
		}, nil
	}
	timeout := toolTimeout(NormalizeBudget(execCtx.Budget).Tool)
	execCtx.RunLoopState = RunLoopStateWaitingSubmit
	execCtx.RunControl.TransitionState(RunLoopStateWaitingSubmit)
	waitStarted := time.Now()

	result, err := execCtx.RunControl.AwaitSubmitWithTimeout(ctx, awaitingID, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			elapsedMs := time.Since(waitStarted).Milliseconds()
			timeoutMs := timeout.Milliseconds()
			detailedMsg := resolveFrontendTimeoutMessage(toolName, awaitingID, timeoutMs, elapsedMs)
			return ToolExecutionResult{
				Output:     detailedMsg,
				Structured: AwaitingErrorAnswer(strings.TrimSpace(AnyStringNode(args["mode"])), "timeout", "等待项已超时"),
				Error:      "frontend_submit_timeout",
				ExitCode:   -1,
			}, nil
		}
		return ToolExecutionResult{}, err
	}
	execCtx.RunLoopState = RunLoopStateToolExecuting
	execCtx.RunControl.TransitionState(RunLoopStateToolExecuting)

	normalized, normalizeErr := handler.NormalizeSubmit(args, result.Request.Params)
	if normalizeErr != nil {
		payload := NewErrorPayload(
			"frontend_submit_invalid_payload",
			normalizeErr.Error(),
			ErrorScopeFrontendSubmit,
			ErrorCategoryTool,
			map[string]any{
				"awaitingId": awaitingID,
				"toolName":   toolName,
				"params":     result.Request.Params,
			},
		)
		return ToolExecutionResult{
			Output:     marshalJSON(payload),
			Structured: payload,
			Error:      "frontend_submit_invalid_payload",
			ExitCode:   -1,
			SubmitInfo: &SubmitInfo{
				RunID:      result.Request.RunID,
				AwaitingID: result.Request.AwaitingID,
				Params:     result.Request.Params,
			},
		}, nil
	}
	data, _ := json.Marshal(normalized)
	rawParams := result.Request.Params
	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		rawParams = nil
	}
	return ToolExecutionResult{
		Output:     string(data),
		Structured: normalized,
		RawParams:  rawParams,
		ExitCode:   0,
		SubmitInfo: &SubmitInfo{
			RunID:      result.Request.RunID,
			AwaitingID: result.Request.AwaitingID,
			Params:     result.Request.Params,
		},
	}, nil
}

func resolveFrontendTimeoutMessage(toolName string, awaitingID string, timeoutMs int64, elapsedMs int64) string {
	if toolName == "" {
		toolName = "unknown"
	}
	if awaitingID == "" {
		awaitingID = "unknown"
	}
	return "Frontend tool submit timeout: tool=" + toolName + ", awaitingId=" + awaitingID + ", elapsedMs=" + formatInt64(elapsedMs) + ", timeoutMs=" + formatInt64(timeoutMs)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func marshalJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
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
