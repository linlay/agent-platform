package runops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/toolinput"
)

const (
	StartToolName     = "chat_start"
	StatusToolName    = "chat_get_status"
	InterruptToolName = "chat_interrupt"
)

type idempotentStart struct {
	done          chan struct{}
	requestDigest string
	runID         string
	snapshot      contracts.RunSnapshot
	err           error
}

// startReview is the frozen baseline of a chat_start invocation that was sent
// to human review. It is kept until the caller Run ends: that invocation may
// only ever start by consuming its one-shot approval against this exact
// baseline, however often it is replayed. Re-evaluating against a changed
// parent level takes a new tool call.
type startReview struct {
	parentAccessLevel   string
	parentAccessVersion int64
	approvalDigest      string
	approval            contracts.ToolApproval
}

const startApprovalAction = "start"

type ToolHandler struct {
	service Runtime
	runs    contracts.RunManager

	mu          sync.Mutex
	idempotency map[string]*idempotentStart
	reviews     map[string]startReview
}

type Runtime interface {
	PrepareRunStart(context.Context, contracts.RunStartRequest) (contracts.RunStartPlan, error)
	StartRun(context.Context, contracts.RunStartRequest) (contracts.RunSnapshot, error)
	GetRunStatus(string) (contracts.RunSnapshot, error)
	Interrupt(context.Context, runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error)
}

func NewToolHandler(service Runtime, runs contracts.RunManager) *ToolHandler {
	return &ToolHandler{
		service:     service,
		runs:        runs,
		idempotency: map[string]*idempotentStart{},
		reviews:     map[string]startReview{},
	}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{StartToolName, StatusToolName, InterruptToolName}
}

func (h *ToolHandler) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	origin, errResult := h.callerOrigin(execCtx)
	if errResult != nil {
		return *errResult, nil
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case StartToolName:
		return h.query(ctx, args, origin, execCtx)
	case StatusToolName:
		return h.status(args, origin)
	case InterruptToolName:
		return h.interrupt(ctx, args, origin)
	default:
		return errorResult("invalid_tool", "tool must be chat_start, chat_get_status, or chat_interrupt"), nil
	}
}

func (h *ToolHandler) callerOrigin(execCtx *contracts.ExecutionContext) (contracts.RunOrigin, *contracts.ToolExecutionResult) {
	if execCtx == nil {
		result := errorResult("run_context_required", "Chat tools require an active main Agent run")
		return contracts.RunOrigin{}, &result
	}
	session := execCtx.Session
	if session.RunOrigin != nil {
		result := errorResult("run_chaining_not_allowed", "a run created by chat_start cannot call Chat tools; return the request to the initiating ordinary main Agent Run to query, start or interrupt its owned runs; changing runId cannot remove this restriction")
		return contracts.RunOrigin{}, &result
	}
	owner := contracts.ResolveRunOwner(session.RunOwner)
	callerAgentKey := strings.TrimSpace(session.AgentKey)
	if strings.TrimSpace(session.SubTaskID) != "" ||
		strings.TrimSpace(session.TeamID) != "" ||
		owner.IsTeam() ||
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

func startKey(origin contracts.RunOrigin) string {
	return origin.RunID + "\x00" + origin.ToolID
}

func startRequest(args map[string]any, origin contracts.RunOrigin) (contracts.RunStartRequest, error) {
	request, err := parseQueryArguments(args)
	if err != nil {
		return request, err
	}
	request.Message = strings.TrimSpace(contracts.AnyStringNode(args["message"]))
	request.AgentKey = strings.TrimSpace(contracts.AnyStringNode(args["agentKey"]))
	request.TeamID = strings.TrimSpace(contracts.AnyStringNode(args["teamId"]))
	request.ChatID = strings.TrimSpace(contracts.AnyStringNode(args["chatId"]))
	request.Origin = origin
	if request.Message == "" || (request.AgentKey == "") == (request.TeamID == "") {
		return request, &contracts.RunToolError{Code: "invalid_request", Message: "message and exactly one of agentKey or teamId are required"}
	}
	if origin.RunID == "" || origin.ToolID == "" {
		return request, &contracts.RunToolError{Code: "run_context_required", Message: "query requires parent runId and toolId"}
	}
	return request, nil
}

// PrepareToolApproval requires human review whenever chat_start asks for a
// level above the caller Run's live level. The review can never be answered
// by an access level, a rule or the model: AllowAutoApprove stays false.
// Requests that are invalid or need no review return nil and are handled,
// unchanged, by Invoke.
func (h *ToolHandler) PrepareToolApproval(ctx context.Context, tool string, args map[string]any, execCtx *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	if strings.ToLower(strings.TrimSpace(tool)) != StartToolName {
		return nil, nil
	}
	origin, errResult := h.callerOrigin(execCtx)
	if errResult != nil {
		return nil, nil
	}
	request, err := startRequest(args, origin)
	if err != nil {
		return nil, nil
	}
	key := startKey(origin)
	h.mu.Lock()
	h.cleanupIdempotencyLocked()
	_, started := h.idempotency[key]
	frozen, reviewed := h.reviews[key]
	h.mu.Unlock()
	if started {
		return nil, nil
	}
	if reviewed {
		// Never re-plan a reviewed call: show the review it was frozen with.
		approval := frozen.approval
		approval.Form = contracts.CloneMap(approval.Form)
		return &approval, nil
	}
	plan, err := h.service.PrepareRunStart(ctx, request)
	if err != nil || !plan.RequiresApproval {
		return nil, nil
	}
	target := map[string]any{"type": "agent", "key": request.AgentKey, "name": plan.TargetName}
	if request.TeamID != "" {
		target = map[string]any{"type": "team", "key": request.TeamID, "name": plan.TargetName}
	}
	form := map[string]any{
		"action":            startApprovalAction,
		"caller":            map[string]any{"agentKey": origin.AgentKey, "chatId": origin.ChatID, "runId": origin.RunID},
		"target":            target,
		"continuesChat":     request.ChatID != "",
		"chatId":            request.ChatID,
		"chatName":          firstNonEmpty(request.ChatName, plan.ChatName),
		"parentAccessLevel": plan.ParentAccessLevel,
		"accessLevel":       plan.AccessLevel,
		"message":           request.Message,
		"mustUseSkills":     append([]string{}, request.MustUseSkills...),
	}
	approval := contracts.ToolApproval{
		Fingerprint: contracts.ToolApprovalFingerprint(execCtx, StartToolName, startApprovalAction, plan.ApprovalDigest),
		Title:       StartToolName + " / " + plan.AccessLevel,
		Form:        form,
	}
	h.mu.Lock()
	if existing, raced := h.reviews[key]; raced {
		approval = existing.approval
	} else {
		h.reviews[key] = startReview{
			parentAccessLevel:   plan.ParentAccessLevel,
			parentAccessVersion: plan.ParentAccessVersion,
			approvalDigest:      plan.ApprovalDigest,
			approval:            approval,
		}
	}
	h.mu.Unlock()
	approval.Form = contracts.CloneMap(approval.Form)
	return &approval, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (h *ToolHandler) query(
	ctx context.Context,
	args map[string]any,
	origin contracts.RunOrigin,
	execCtx *contracts.ExecutionContext,
) (contracts.ToolExecutionResult, error) {
	request, parseErr := startRequest(args, origin)
	if parseErr != nil {
		return resultFromError(parseErr), nil
	}
	key := startKey(origin)
	requestDigest := contracts.RunStartRequestDigest(request)
	start, leader := h.beginIdempotentStart(key, requestDigest)
	if start.requestDigest != requestDigest {
		return resultFromError(&contracts.RunToolError{Code: "idempotency_conflict", ExecutionState: "not_started",
			Message: "this tool call already started a run with different arguments"}), nil
	}
	if !leader {
		select {
		case <-ctx.Done():
			return errorResult("chat_start_cancelled", ctx.Err().Error()), nil
		case <-start.done:
		}
		// An unconfirmed start is reconciled against the recorded Run and is
		// never started again.
		if start.runID == "" {
			return resultFromError(start.err), nil
		}
		snapshot, err := h.service.GetRunStatus(start.runID)
		if err != nil {
			if start.err != nil {
				return resultFromError(start.err), nil
			}
			snapshot = start.snapshot
		}
		return successResult("query", true, "accepted", snapshot), nil
	}

	// The frozen review is deliberately not removed here; see startReview.
	h.mu.Lock()
	review, reviewed := h.reviews[key]
	h.mu.Unlock()
	if reviewed {
		// The approval lives in the trusted execution context and is consumed
		// synchronously by Runtime against the digest Runtime computes.
		request.Review = &contracts.RunStartReview{
			ParentAccessLevel:   review.parentAccessLevel,
			ParentAccessVersion: review.parentAccessVersion,
			ApprovalDigest:      review.approvalDigest,
			Consume: func(approvalDigest string) bool {
				return contracts.ConsumeToolApproval(execCtx, contracts.ToolApprovalFingerprint(execCtx, StartToolName, startApprovalAction, approvalDigest))
			},
		}
	}
	snapshot, err := h.service.StartRun(ctx, request)
	h.finishIdempotentStart(key, start, snapshot, err)
	if err != nil {
		return resultFromError(err), nil
	}
	h.cleanupIdempotencyWithParent(key, start, execCtx.RunControl)
	return successResult("query", true, "accepted", snapshot), nil
}

func (h *ToolHandler) status(args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
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
			RunID: runID, ChatID: snapshot.ChatID, AgentKey: snapshot.AgentKey, TeamID: snapshot.TeamID,
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
	result := errorResult("run_not_owned", "run was not created by chat_start for this caller and subject")
	return &result
}

func (h *ToolHandler) beginIdempotentStart(key string, requestDigest string) (*idempotentStart, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupIdempotencyLocked()
	if existing, ok := h.idempotency[key]; ok {
		return existing, false
	}
	start := &idempotentStart{done: make(chan struct{}), requestDigest: requestDigest}
	h.idempotency[key] = start
	return start, true
}

func (h *ToolHandler) finishIdempotentStart(key string, start *idempotentStart, snapshot contracts.RunSnapshot, err error) {
	h.mu.Lock()
	start.snapshot = snapshot
	start.runID = snapshot.RunID
	start.err = err
	if err != nil {
		// Only a start that definitely did not happen may be attempted again.
		// An unconfirmed start keeps its record so retries reconcile instead.
		var typed *contracts.RunToolError
		if errors.As(err, &typed) && typed.ExecutionState == "unknown" {
			start.runID = typed.RunID
		} else {
			delete(h.idempotency, key)
		}
	}
	close(start.done)
	h.mu.Unlock()
}

func (h *ToolHandler) cleanupIdempotencyLocked() {
	if h.runs == nil {
		return
	}
	parentDone := func(key string) bool {
		parentRunID, _, _ := strings.Cut(key, "\x00")
		status, ok := h.runs.RunStatus(parentRunID)
		return !ok || status.CompletedAt != 0
	}
	for key := range h.idempotency {
		if parentDone(key) {
			delete(h.idempotency, key)
		}
	}
	// A review whose caller Run ended can never be approved or started.
	for key := range h.reviews {
		if parentDone(key) {
			delete(h.reviews, key)
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
