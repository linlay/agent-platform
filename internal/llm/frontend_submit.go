package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
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
		payload := apperrors.Payload(
			apperrors.CodeFrontendToolHandlerNotRegistered,
			"frontend tool handler not registered: "+toolName,
			apperrors.WithScope(apperrors.ScopeFrontendSubmit),
			apperrors.WithCategory(apperrors.CategoryTool),
			apperrors.WithDiagnostics(map[string]any{
				"awaitingId": awaitingID,
				"toolName":   toolName,
			}),
		)
		return ToolExecutionResult{
			Output:     marshalJSON(payload),
			Structured: payload,
			Error:      "frontend_tool_handler_not_registered",
			ExitCode:   -1,
		}, nil
	}
	timeout := frontendSubmitTimeout(execCtx)
	execCtx.RunLoopState = RunLoopStateWaitingSubmit
	execCtx.RunControl.TransitionState(RunLoopStateWaitingSubmit)
	waitStarted := time.Now()

	result, err := execCtx.RunControl.AwaitSubmitWithTimeout(ctx, awaitingID, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			elapsed := time.Since(waitStarted).Milliseconds() / 1000
			timeoutSec := int64(timeout.Seconds())
			detailedMsg := resolveFrontendTimeoutMessage(toolName, awaitingID, timeoutSec, elapsed)
			return ToolExecutionResult{
				Output:     detailedMsg,
				Structured: AwaitingTimeoutAnswer(strings.TrimSpace(AnyStringNode(args["mode"])), timeoutSec, elapsed),
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
		payload := apperrors.Payload(
			apperrors.CodeFrontendSubmitInvalidPayload,
			normalizeErr.Error(),
			apperrors.WithScope(apperrors.ScopeFrontendSubmit),
			apperrors.WithCategory(apperrors.CategoryTool),
			apperrors.WithDiagnostics(map[string]any{
				"awaitingId": awaitingID,
				"toolName":   toolName,
				"params":     result.Request.Params,
			}),
		)
		return ToolExecutionResult{
			Output:     formatToolErrorOutput("frontend_submit_invalid_payload", normalizeErr.Error()),
			Structured: payload,
			Error:      "frontend_submit_invalid_payload",
			ExitCode:   -1,
			SubmitInfo: &SubmitInfo{
				RunID:      result.Request.RunID,
				AwaitingID: result.Request.AwaitingID,
				SubmitID:   result.Request.SubmitID,
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
			SubmitID:   result.Request.SubmitID,
			Params:     result.Request.Params,
		},
	}, nil
}

func frontendSubmitTimeout(execCtx *ExecutionContext) time.Duration {
	if execCtx != nil && execCtx.RunControl != nil {
		if awaitingCtx, ok := execCtx.RunControl.LookupAwaiting(execCtx.CurrentToolID); ok && awaitingCtx.Timeout > 0 {
			return time.Duration(awaitingCtx.Timeout) * time.Second
		}
	}
	budget := Budget{}
	if execCtx != nil {
		budget = NormalizeBudget(execCtx.Budget)
	}
	mode := argsModeFromExecContext(execCtx)
	return time.Duration(ResolveHITLTimeout(mode, 0, budget)) * time.Second
}

func argsModeFromExecContext(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(execCtx.CurrentToolName), "ask_user_question") {
		return "question"
	}
	return "form"
}

func resolveFrontendTimeoutMessage(toolName string, awaitingID string, timeout int64, elapsed int64) string {
	if toolName == "" {
		toolName = "unknown"
	}
	if awaitingID == "" {
		awaitingID = "unknown"
	}
	return "Frontend tool submit timeout: tool=" + toolName + ", awaitingId=" + awaitingID + ", elapsed=" + formatInt64(elapsed) + ", timeout=" + formatInt64(timeout)
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
