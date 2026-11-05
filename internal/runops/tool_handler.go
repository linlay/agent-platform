package runops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/toolinput"
)

const (
	StatusToolName    = "chat_get_status"
	InterruptToolName = "chat_interrupt"
)

type ToolHandler struct {
	mailbox *mailbox.Service
	service Runtime
	runs    contracts.RunManager
}

type Runtime interface {
	GetRunStatus(string) (contracts.RunSnapshot, error)
	Interrupt(context.Context, runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error)
}

func NewToolHandler(service Runtime, runs contracts.RunManager) *ToolHandler {
	return &ToolHandler{
		service: service,
		runs:    runs,
	}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{StatusToolName, InterruptToolName, "chat_send", "chat_notify", "chat_submit"}
}

func (h *ToolHandler) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	if toolName == "chat_send" || toolName == "chat_notify" || toolName == "chat_submit" {
		return h.invokeMail(ctx, toolName, args, execCtx)
	}
	origin, errResult := h.callerOrigin(execCtx)
	if errResult != nil {
		return *errResult, nil
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case StatusToolName:
		return h.status(args, origin)
	case InterruptToolName:
		return h.interrupt(ctx, args, origin)
	default:
		return errorResult("invalid_tool", "unknown Chat tool"), nil
	}
}

func (h *ToolHandler) callerOrigin(execCtx *contracts.ExecutionContext) (contracts.RunOrigin, *contracts.ToolExecutionResult) {
	if execCtx == nil {
		result := errorResult("run_context_required", "Chat tools require an active main Agent run")
		return contracts.RunOrigin{}, &result
	}
	session := execCtx.Session
	if session.RunOrigin != nil {
		result := errorResult("run_chaining_not_allowed", "a run opened by a letter cannot control other Runs; ask the initiating Agent")
		return contracts.RunOrigin{}, &result
	}
	owner := contracts.ResolveRunOwner(session.RunOwner)
	callerAgentKey := strings.TrimSpace(session.AgentKey)
	if strings.TrimSpace(session.SubTaskID) != "" ||
		callerAgentKey == "" ||
		owner.AgentKey != callerAgentKey {
		result := errorResult("run_caller_not_allowed", "Chat tools are only available to an ordinary main Agent root run")
		return contracts.RunOrigin{}, &result
	}
	return contracts.RunOrigin{
		AgentKey: callerAgentKey,
		Subject:  strings.TrimSpace(session.Subject),
		ChatID:   strings.TrimSpace(session.ChatID),
		RunID:    strings.TrimSpace(session.RunID),
		ToolID:   strings.TrimSpace(execCtx.CurrentToolID),
	}, nil
}

func startRequest(args map[string]any, origin contracts.RunOrigin, execCtx *contracts.ExecutionContext) (contracts.RunStartRequest, error) {
	request, err := parseQueryArguments(args)
	if err != nil {
		return request, err
	}
	request.Message = strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	request.AgentKey = strings.TrimSpace(contracts.AnyStringNode(args["agentKey"]))
	request.ChatID = strings.TrimSpace(contracts.AnyStringNode(args["chatId"]))
	request.Origin = origin
	// A start that stays on the calling Agent resolves each omitted field from
	// the actual executing model before idempotency. Another Agent
	// keeps its own configured model and effort unless the call names them.
	sameAgent := request.AgentKey == "" || request.AgentKey == strings.TrimSpace(origin.AgentKey)
	if sameAgent && execCtx != nil && execCtx.CurrentModel != nil {
		if request.ModelKey == "" {
			request.ModelKey = execCtx.CurrentModel.Key
		}
		if request.ReasoningEffort == "" {
			request.ReasoningEffort = execCtx.CurrentModel.ReasoningEffort
		}
	}
	if origin.RunID == "" || origin.ToolID == "" {
		return request, &contracts.RunToolError{Code: "run_context_required", Message: "query requires parent runId and toolId"}
	}
	return contracts.NormalizeRunStartRequest(request)
}

func (h *ToolHandler) status(args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
	if _, ok := args["mailId"]; ok {
		return h.mailStatus(args, origin)
	}
	if err := toolinput.Validate(args, map[string]string{"runId": "s!"}, ""); err != nil {
		return resultFromError(err), nil
	}
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	if runID == "" {
		return errorResult("invalid_request", "runId is required"), nil
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result, nil
	}
	snapshot, err := h.service.GetRunStatus(runID)
	if err != nil {
		return resultFromError(err), nil
	}
	return successResult("status", true, snapshot.Status, snapshot), nil
}

func (h *ToolHandler) interrupt(ctx context.Context, args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
	if err := toolinput.Validate(args, map[string]string{"runId": "s!", "message": "s"}, ""); err != nil {
		return resultFromError(err), nil
	}
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	if runID == "" {
		return errorResult("invalid_request", "runId is required"), nil
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result, nil
	}
	snapshot, err := h.service.GetRunStatus(runID)
	if err != nil {
		return resultFromError(err), nil
	}
	message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	response, err := h.service.Interrupt(ctx, runtimetypes.InterruptCommand{
		RunRef: runtimetypes.RunRef{
			RunID: runID, ChatID: snapshot.ChatID, AgentKey: snapshot.AgentKey,
		},
		Message: message,
		Detail:  message,
	})
	if err != nil {
		return resultFromError(err), nil
	}
	snapshot, _ = h.service.GetRunStatus(runID)
	return successResult("interrupt", response.Accepted, response.Status, snapshot), nil
}

func (h *ToolHandler) requireOwnedRun(runID string, origin contracts.RunOrigin) *contracts.ToolExecutionResult {
	if h.mailbox != nil && h.mailbox.OwnsRun(runID, origin.AgentKey, origin.Subject) {
		return nil
	}
	snapshot, err := h.service.GetRunStatus(runID)
	if err == nil && snapshot.Origin != nil &&
		snapshot.Origin.AgentKey == origin.AgentKey &&
		snapshot.Origin.Subject == origin.Subject {
		return nil
	}
	if err != nil {
		var typed *contracts.RunToolError
		if ok := errors.As(err, &typed); ok && typed.Code == "run_not_found" {
			result := errorResult("run_not_found", "run not found")
			return &result
		}
		result := resultFromError(err)
		return &result
	}
	result := errorResult("run_not_owned", "run was not opened by a letter for this caller and subject")
	return &result
}

func successResult(action string, accepted bool, status string, run contracts.RunSnapshot) contracts.ToolExecutionResult {
	payload := map[string]any{
		"action":   action,
		"accepted": accepted,
		"status":   status,
		"run":      runPayload(run),
	}
	data, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{Output: string(data), Structured: payload, ExitCode: 0}
}

func runPayload(run contracts.RunSnapshot) map[string]any {
	data, _ := json.Marshal(run)
	payload := map[string]any{}
	_ = json.Unmarshal(data, &payload)
	return payload
}

func resultFromError(err error) contracts.ToolExecutionResult {
	var input *toolinput.Error
	if errors.As(err, &input) {
		r := errorResult("invalid_request", input.Error())
		for k, v := range input.Details() {
			r.Structured[k] = v
		}
		r.Structured["stage"] = "admission"
		r.Structured["executionState"] = "not_started"
		r.Output = contracts.CompactToolModelOutput(r.Structured, "")
		return r
	}
	var typed *contracts.RunToolError
	if errors.As(err, &typed) {
		r := errorResult(typed.Code, typed.Message)
		if typed.ExecutionState != "" {
			r.Structured["executionState"] = typed.ExecutionState
		}
		if typed.Retryable {
			r.Structured["retryable"] = true
		}
		if typed.RunID != "" {
			r.Structured["run"] = map[string]any{"runId": typed.RunID, "chatId": typed.ChatID}
		}
		data, _ := json.Marshal(r.Structured)
		r.Output = string(data)
		return r
	}
	if err == nil {
		return errorResult("internal_error", "run tool failed")
	}
	return errorResult("internal_error", err.Error())
}

func errorResult(code string, message string) contracts.ToolExecutionResult {
	payload := map[string]any{
		"error":   strings.TrimSpace(code),
		"message": strings.TrimSpace(message),
	}
	data, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		Error:      strings.TrimSpace(code),
		ExitCode:   -1,
	}
}
