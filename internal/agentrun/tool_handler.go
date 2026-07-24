package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const ToolName = "agent_run"

type idempotentStart struct {
	done     chan struct{}
	runID    string
	snapshot contracts.AgentRunSnapshot
	err      error
}

type ToolHandler struct {
	service contracts.AgentRunService
	runs    contracts.RunManager

	mu          sync.Mutex
	idempotency map[string]*idempotentStart
}

func NewToolHandler(service contracts.AgentRunService, runs contracts.RunManager) *ToolHandler {
	return &ToolHandler{
		service:     service,
		runs:        runs,
		idempotency: map[string]*idempotentStart{},
	}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{ToolName}
}

func (h *ToolHandler) Invoke(ctx context.Context, _ string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	origin, errResult := h.callerOrigin(execCtx)
	if errResult != nil {
		return *errResult, nil
	}
	action := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(args["action"])))
	switch action {
	case "query":
		return h.query(ctx, args, origin, execCtx.RunControl)
	case "status":
		return h.status(args, origin)
	case "submit":
		return h.submit(args, origin)
	case "steer":
		return h.steer(args, origin)
	case "interrupt":
		return h.interrupt(args, origin)
	default:
		return errorResult("invalid_action", "action must be query, status, submit, steer, or interrupt"), nil
	}
}

func (h *ToolHandler) callerOrigin(execCtx *contracts.ExecutionContext) (contracts.AgentRunOrigin, *contracts.ToolExecutionResult) {
	if execCtx == nil {
		result := errorResult("agent_run_context_required", "agent_run requires an active main Agent run")
		return contracts.AgentRunOrigin{}, &result
	}
	session := execCtx.Session
	if session.AgentRunOrigin != nil {
		result := errorResult("agent_run_chaining_not_allowed", "a run created by agent_run cannot call agent_run")
		return contracts.AgentRunOrigin{}, &result
	}
	owner := contracts.ResolveRunOwner(session.RunOwner)
	callerAgentKey := strings.TrimSpace(session.AgentKey)
	if strings.TrimSpace(session.SubTaskID) != "" ||
		strings.TrimSpace(session.TeamID) != "" ||
		owner.IsTeam() ||
		callerAgentKey == "" ||
		owner.AgentKey != callerAgentKey {
		result := errorResult("agent_run_caller_not_allowed", "agent_run is only available to an ordinary main Agent root run")
		return contracts.AgentRunOrigin{}, &result
	}
	return contracts.AgentRunOrigin{
		CallerAgentKey: callerAgentKey,
		Subject:        strings.TrimSpace(session.Subject),
		ParentChatID:   strings.TrimSpace(session.ChatID),
		ParentRunID:    strings.TrimSpace(session.RunID),
		ToolID:         strings.TrimSpace(execCtx.CurrentToolID),
	}, nil
}

func (h *ToolHandler) query(
	ctx context.Context,
	args map[string]any,
	origin contracts.AgentRunOrigin,
	parentControl *contracts.RunControl,
) (contracts.ToolExecutionResult, error) {
	message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	agentKey := strings.TrimSpace(contracts.AnyStringNode(args["agentKey"]))
	teamID := strings.TrimSpace(contracts.AnyStringNode(args["teamId"]))
	chatID := strings.TrimSpace(contracts.AnyStringNode(args["chatId"]))
	if message == "" || (agentKey == "") == (teamID == "") {
		return errorResult("invalid_request", "message and exactly one of agentKey or teamId are required"), nil
	}
	if origin.ParentRunID == "" || origin.ToolID == "" {
		return errorResult("agent_run_context_required", "query requires parent runId and toolId"), nil
	}

	key := origin.ParentRunID + "\x00" + origin.ToolID
	start, leader := h.beginIdempotentStart(key)
	if !leader {
		select {
		case <-ctx.Done():
			return errorResult("agent_run_cancelled", ctx.Err().Error()), nil
		case <-start.done:
		}
		if start.err != nil {
			return resultFromError(start.err), nil
		}
		snapshot, err := h.service.AgentRunStatus(start.runID)
		if err != nil {
			snapshot = start.snapshot
		}
		return successResult("query", true, "accepted", snapshot), nil
	}

	snapshot, err := h.service.StartAgentRun(ctx, contracts.AgentRunStartRequest{
		AgentKey: agentKey,
		TeamID:   teamID,
		ChatID:   chatID,
		Message:  message,
		Origin:   origin,
	})
	h.finishIdempotentStart(key, start, snapshot, err)
	if err != nil {
		return resultFromError(err), nil
	}
	h.cleanupIdempotencyWithParent(key, start, parentControl)
	return successResult("query", true, "accepted", snapshot), nil
}

func (h *ToolHandler) status(args map[string]any, origin contracts.AgentRunOrigin) (contracts.ToolExecutionResult, error) {
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	if runID == "" {
		return errorResult("invalid_request", "runId is required"), nil
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result, nil
	}
	snapshot, err := h.service.AgentRunStatus(runID)
	if err != nil {
		return resultFromError(err), nil
	}
	return successResult("status", true, snapshot.Status, snapshot), nil
}

func (h *ToolHandler) submit(args map[string]any, origin contracts.AgentRunOrigin) (contracts.ToolExecutionResult, error) {
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	awaitingID := strings.TrimSpace(contracts.AnyStringNode(args["awaitingId"]))
	if runID == "" || awaitingID == "" {
		return errorResult("invalid_request", "runId and awaitingId are required"), nil
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result, nil
	}
	params, err := api.EncodeSubmitParams(args["params"])
	if err != nil {
		return errorResult("invalid_request", err.Error()), nil
	}
	snapshot, err := h.service.AgentRunStatus(runID)
	if err != nil {
		return resultFromError(err), nil
	}
	response, err := h.service.AgentRunSubmitQuestion(api.SubmitRequest{
		ChatID:     snapshot.ChatID,
		RunID:      runID,
		AgentKey:   snapshot.AgentKey,
		TeamID:     snapshot.TeamID,
		AwaitingID: awaitingID,
		Params:     params,
	})
	if err != nil {
		return resultFromError(err), nil
	}
	snapshot, _ = h.service.AgentRunStatus(runID)
	return successResult("submit", response.Accepted, response.Status, snapshot), nil
}

func (h *ToolHandler) steer(args map[string]any, origin contracts.AgentRunOrigin) (contracts.ToolExecutionResult, error) {
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	if runID == "" || message == "" {
		return errorResult("invalid_request", "runId and message are required"), nil
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result, nil
	}
	snapshot, err := h.service.AgentRunStatus(runID)
	if err != nil {
		return resultFromError(err), nil
	}
	response, err := h.service.AgentRunSteer(api.SteerRequest{
		RunID:    runID,
		ChatID:   snapshot.ChatID,
		AgentKey: snapshot.AgentKey,
		TeamID:   snapshot.TeamID,
		Message:  message,
	})
	if err != nil {
		return resultFromError(err), nil
	}
	snapshot, _ = h.service.AgentRunStatus(runID)
	return successResult("steer", response.Accepted, response.Status, snapshot), nil
}

func (h *ToolHandler) interrupt(args map[string]any, origin contracts.AgentRunOrigin) (contracts.ToolExecutionResult, error) {
	runID := strings.TrimSpace(contracts.AnyStringNode(args["runId"]))
	if runID == "" {
		return errorResult("invalid_request", "runId is required"), nil
	}
	if result := h.requireOwnedRun(runID, origin); result != nil {
		return *result, nil
	}
	snapshot, err := h.service.AgentRunStatus(runID)
	if err != nil {
		return resultFromError(err), nil
	}
	message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	response, err := h.service.AgentRunInterrupt(api.InterruptRequest{
		RunID:           runID,
		ChatID:          snapshot.ChatID,
		AgentKey:        snapshot.AgentKey,
		TeamID:          snapshot.TeamID,
		Message:         message,
		InterruptDetail: message,
	})
	if err != nil {
		return resultFromError(err), nil
	}
	snapshot, _ = h.service.AgentRunStatus(runID)
	return successResult("interrupt", response.Accepted, response.Status, snapshot), nil
}

func (h *ToolHandler) requireOwnedRun(runID string, origin contracts.AgentRunOrigin) *contracts.ToolExecutionResult {
	snapshot, err := h.service.AgentRunStatus(runID)
	if err == nil && snapshot.Origin != nil &&
		snapshot.Origin.CallerAgentKey == origin.CallerAgentKey &&
		snapshot.Origin.Subject == origin.Subject {
		return nil
	}
	if err != nil {
		var typed *contracts.AgentRunError
		if ok := errors.As(err, &typed); ok && typed.Code == "run_not_found" {
			result := errorResult("run_not_found", "run not found")
			return &result
		}
		result := resultFromError(err)
		return &result
	}
	result := errorResult("run_not_owned", "run was not created by agent_run for this caller and subject")
	return &result
}

func (h *ToolHandler) beginIdempotentStart(key string) (*idempotentStart, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupIdempotencyLocked()
	if existing, ok := h.idempotency[key]; ok {
		return existing, false
	}
	start := &idempotentStart{done: make(chan struct{})}
	h.idempotency[key] = start
	return start, true
}

func (h *ToolHandler) finishIdempotentStart(key string, start *idempotentStart, snapshot contracts.AgentRunSnapshot, err error) {
	h.mu.Lock()
	start.snapshot = snapshot
	start.runID = snapshot.RunID
	start.err = err
	if err != nil {
		delete(h.idempotency, key)
	}
	close(start.done)
	h.mu.Unlock()
}

func (h *ToolHandler) cleanupIdempotencyLocked() {
	if h.runs == nil {
		return
	}
	for key := range h.idempotency {
		parentRunID, _, _ := strings.Cut(key, "\x00")
		status, ok := h.runs.RunStatus(parentRunID)
		if !ok || status.CompletedAt != 0 {
			delete(h.idempotency, key)
		}
	}
}

func (h *ToolHandler) cleanupIdempotencyWithParent(key string, start *idempotentStart, parentControl *contracts.RunControl) {
	if parentControl == nil {
		return
	}
	go func() {
		<-parentControl.Context().Done()
		h.mu.Lock()
		if h.idempotency[key] == start {
			delete(h.idempotency, key)
		}
		h.mu.Unlock()
	}()
}

func successResult(action string, accepted bool, status string, run contracts.AgentRunSnapshot) contracts.ToolExecutionResult {
	payload := map[string]any{
		"action":   action,
		"accepted": accepted,
		"status":   status,
		"run":      runPayload(run),
	}
	data, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{Output: string(data), Structured: payload, ExitCode: 0}
}

func runPayload(run contracts.AgentRunSnapshot) map[string]any {
	data, _ := json.Marshal(run)
	payload := map[string]any{}
	_ = json.Unmarshal(data, &payload)
	return payload
}

func resultFromError(err error) contracts.ToolExecutionResult {
	var typed *contracts.AgentRunError
	if errors.As(err, &typed) {
		return errorResult(typed.Code, typed.Message)
	}
	if err == nil {
		return errorResult("internal_error", "agent_run failed")
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
