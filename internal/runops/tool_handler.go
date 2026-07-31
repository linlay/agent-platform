package runops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const (
	QueryToolName     = "run_query"
	StatusToolName    = "run_status"
	InterruptToolName = "run_interrupt"
)

type idempotentStart struct {
	done     chan struct{}
	runID    string
	snapshot contracts.RunSnapshot
	err      error
}

type ToolHandler struct {
	service contracts.RunToolService
	runs    contracts.RunManager

	mu          sync.Mutex
	idempotency map[string]*idempotentStart
}

func NewToolHandler(service contracts.RunToolService, runs contracts.RunManager) *ToolHandler {
	return &ToolHandler{
		service:     service,
		runs:        runs,
		idempotency: map[string]*idempotentStart{},
	}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{QueryToolName, StatusToolName, InterruptToolName}
}

func (h *ToolHandler) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	origin, errResult := h.callerOrigin(execCtx)
	if errResult != nil {
		return *errResult, nil
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case QueryToolName:
		return h.query(ctx, args, origin, execCtx.RunControl)
	case StatusToolName:
		return h.status(args, origin)
	case InterruptToolName:
		return h.interrupt(args, origin)
	default:
		return errorResult("invalid_tool", "tool must be run_query, run_status, or run_interrupt"), nil
	}
}

func (h *ToolHandler) callerOrigin(execCtx *contracts.ExecutionContext) (contracts.RunOrigin, *contracts.ToolExecutionResult) {
	if execCtx == nil {
		result := errorResult("run_context_required", "run tools require an active main Agent run")
		return contracts.RunOrigin{}, &result
	}
	session := execCtx.Session
	if session.RunOrigin != nil {
		result := errorResult("run_chaining_not_allowed", "a run created by run_query cannot call run tools")
		return contracts.RunOrigin{}, &result
	}
	owner := contracts.ResolveRunOwner(session.RunOwner)
	callerAgentKey := strings.TrimSpace(session.AgentKey)
	if strings.TrimSpace(session.SubTaskID) != "" ||
		strings.TrimSpace(session.TeamID) != "" ||
		owner.IsTeam() ||
		callerAgentKey == "" ||
		owner.AgentKey != callerAgentKey {
		result := errorResult("run_caller_not_allowed", "run tools are only available to an ordinary main Agent root run")
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

func (h *ToolHandler) query(
	ctx context.Context,
	args map[string]any,
	origin contracts.RunOrigin,
	parentControl *contracts.RunControl,
) (contracts.ToolExecutionResult, error) {
	message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	agentKey := strings.TrimSpace(contracts.AnyStringNode(args["agentKey"]))
	teamID := strings.TrimSpace(contracts.AnyStringNode(args["teamId"]))
	chatID := strings.TrimSpace(contracts.AnyStringNode(args["chatId"]))
	if message == "" || (agentKey == "") == (teamID == "") {
		return errorResult("invalid_request", "message and exactly one of agentKey or teamId are required"), nil
	}
	if origin.RunID == "" || origin.ToolID == "" {
		return errorResult("run_context_required", "query requires parent runId and toolId"), nil
	}

	key := origin.RunID + "\x00" + origin.ToolID
	start, leader := h.beginIdempotentStart(key)
	if !leader {
		select {
		case <-ctx.Done():
			return errorResult("run_query_cancelled", ctx.Err().Error()), nil
		case <-start.done:
		}
		if start.err != nil {
			return resultFromError(start.err), nil
		}
		snapshot, err := h.service.GetRunStatus(start.runID)
		if err != nil {
			snapshot = start.snapshot
		}
		return successResult("query", true, "accepted", snapshot), nil
	}

	snapshot, err := h.service.StartRun(ctx, contracts.RunStartRequest{
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

func (h *ToolHandler) status(args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
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

func (h *ToolHandler) interrupt(args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
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
	response, err := h.service.InterruptRun(api.InterruptRequest{
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
	snapshot, _ = h.service.GetRunStatus(runID)
	return successResult("interrupt", response.Accepted, response.Status, snapshot), nil
}

func (h *ToolHandler) requireOwnedRun(runID string, origin contracts.RunOrigin) *contracts.ToolExecutionResult {
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
	result := errorResult("run_not_owned", "run was not created by run_query for this caller and subject")
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

func (h *ToolHandler) finishIdempotentStart(key string, start *idempotentStart, snapshot contracts.RunSnapshot, err error) {
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
	var typed *contracts.RunToolError
	if errors.As(err, &typed) {
		return errorResult(typed.Code, typed.Message)
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
