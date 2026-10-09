package query

import (
	"context"
	"errors"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/catalogview"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/runtime/runstate"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

func runOwnerMatchesChat(summary *chat.Summary, agentKey string, teamID string) bool {
	if summary == nil {
		return true
	}
	if teamID != "" {
		return strings.TrimSpace(summary.AgentKey) == "" && strings.TrimSpace(summary.TeamID) == teamID
	}
	return strings.TrimSpace(summary.TeamID) == "" && strings.TrimSpace(summary.AgentKey) == agentKey
}

func mapRunStatusError(err *statusError) error {
	if err == nil {
		return nil
	}
	code := strings.TrimSpace(err.Code)
	if code == "" {
		switch err.Status {
		case 404:
			code = "run_not_found"
		case 403:
			code = "run_not_owned"
		default:
			code = "invalid_request"
		}
	}
	return runToolError(code, err.Message)
}

func (s *Service) GetRunStatus(runID string) (contracts.RunSnapshot, error) {
	return runstate.Snapshot(s.deps.Runs, s.deps.Chats, runID)
}

// runAccessLevelAcceptor is the authorization acceptance point provided by
// the Run manager. It is required: without it a permission decision could not
// be made atomically with the level it is based on, so starts are refused.
type runAccessLevelAcceptor interface {
	AcceptAtRunAccessLevel(runID string, accept func(accessLevel string, version int64) error) (bool, error)
}

// PrepareRunStart validates a chat_start request and resolves its permission
// against the parent Run's live level. It is read-only: it creates no Chat,
// registers no Run and holds no reservation or catalog lease, so it is safe to
// call before a human review of unbounded duration.
func (s *Service) PrepareRunStart(ctx context.Context, request contracts.RunStartRequest) (contracts.RunStartPlan, error) {
	if err := ctx.Err(); err != nil {
		return contracts.RunStartPlan{}, runNotStarted(runToolError("chat_start_cancelled", err.Error()))
	}
	fail := func(code string, message string) (contracts.RunStartPlan, error) {
		return contracts.RunStartPlan{}, runNotStarted(runToolError(code, message))
	}
	if _, valid := contracts.NormalizeAccessLevel(request.AccessLevel); !valid {
		return fail("invalid_request", "accessLevel must be default, auto_approve, or full_access")
	}
	chatID := strings.TrimSpace(request.ChatID)
	if strings.TrimSpace(request.ChatName) != "" && chatID != "" {
		return fail("invalid_request", "chatName cannot be combined with chatId")
	}
	agentKey := strings.TrimSpace(request.AgentKey)
	teamID := strings.TrimSpace(request.TeamID)
	if strings.TrimSpace(request.Message) == "" || (agentKey == "") == (teamID == "") {
		return fail("invalid_request", "message and exactly one of agentKey or teamId are required")
	}
	if teamID != "" && (strings.TrimSpace(request.ModelKey) != "" || strings.TrimSpace(request.ReasoningEffort) != "") {
		return fail("invalid_request", "modelKey and reasoningEffort are not supported for Team runs")
	}
	plan := contracts.RunStartPlan{RequestedAccessLevel: strings.TrimSpace(request.AccessLevel)}
	if agentKey != "" {
		def, ok := s.deps.Registry.AgentDefinition(agentKey)
		if !ok {
			return fail("agent_not_found", "agent not found")
		}
		plan.TargetName = strings.TrimSpace(def.Name)
		// Reject invalid overrides before requesting a human permission review.
		// Query admission validates again against the definition used to start.
		if request.ModelKey != "" || request.ReasoningEffort != "" {
			if err := s.deps.Proxy.Configure(&def); err != nil {
				return fail("invalid_request", err.Error())
			}
			options := &queryinput.QueryModelOptions{Key: strings.TrimSpace(request.ModelKey), ReasoningEffort: strings.TrimSpace(request.ReasoningEffort)}
			if err := s.ValidateQueryModelOptions(options, def); err != nil {
				return fail("invalid_request", err.Error())
			}
		}
	} else {
		team, ok := catalogview.ResolveTeam(s.deps.Registry, teamID)
		if !ok {
			return fail("team_not_found", "team not found")
		}
		plan.TargetName = strings.TrimSpace(team.Name)
	}
	if chatID != "" {
		summary, err := s.deps.Chats.Summary(chatID)
		if err != nil && !errors.Is(err, chat.ErrChatNotFound) {
			return contracts.RunStartPlan{}, runNotStarted(err)
		}
		if summary != nil && !runOwnerMatchesChat(summary, agentKey, teamID) {
			return fail("target_owner_mismatch", "target identity does not match chat owner")
		}
		if summary != nil {
			plan.ChatName = strings.TrimSpace(summary.ChatName)
		}
	}
	parentRunID := strings.TrimSpace(request.Origin.RunID)
	if parentRunID == "" {
		return fail("run_context_required", "chat_start requires a parent runId")
	}
	// Always read the live control state: neither model input nor the parent
	// session snapshot may decide the permission baseline.
	parent, ok := s.deps.Runs.RunStatus(parentRunID)
	if !ok || parent.CompletedAt != 0 {
		return fail("run_parent_not_active", "the calling Run is no longer active")
	}
	plan.ParentAccessLevel = sessionbuild.NormalizedAccessLevel(parent.AccessLevel)
	plan.ParentAccessVersion = parent.AccessLevelVersion
	plan.AccessLevel, plan.RequiresApproval = contracts.ResolveRunStartAccessLevel(request.AccessLevel, plan.ParentAccessLevel)
	plan.RequestDigest = contracts.RunStartRequestDigest(request)
	plan.ApprovalDigest = contracts.RunStartApprovalDigest(plan.RequestDigest, plan.ParentAccessLevel, plan.ParentAccessVersion, plan.AccessLevel)
	return plan, nil
}

// acceptRunStart is the single authorization decision for a chat_start. Inside
// the parent Run's access-level critical section it re-reads the level, and for
// any request that exceeds it, or that already carries a frozen review,
// validates the baseline and synchronously consumes the one-shot approval.
// Nothing has been created when it fails.
func (s *Service) acceptRunStart(ctx context.Context, request contracts.RunStartRequest, plan contracts.RunStartPlan) (acceptedRunStart, error) {
	accepted := acceptedRunStart{source: "inherited"}
	accept := func(parentLevel string, version int64) error {
		if err := ctx.Err(); err != nil {
			return runToolError("chat_start_cancelled", err.Error())
		}
		parentLevel = sessionbuild.NormalizedAccessLevel(parentLevel)
		accessLevel, escalates := contracts.ResolveRunStartAccessLevel(request.AccessLevel, parentLevel)
		// Record the baseline this decision was actually made against.
		accepted.parentAccessLevel, accepted.parentAccessVersion, accepted.accessLevel = parentLevel, version, accessLevel
		if review := request.Review; review != nil {
			digest := contracts.RunStartApprovalDigest(plan.RequestDigest, parentLevel, version, accessLevel)
			if version != review.ParentAccessVersion || parentLevel != review.ParentAccessLevel || digest != review.ApprovalDigest {
				return &contracts.RunToolError{Code: "run_start_review_stale", Retryable: true,
					Message: "the parent Run permission changed after this start was reviewed; the approval no longer applies. Call chat_start again with the same target, message and accessLevel"}
			}
			if review.Consume == nil || !review.Consume(digest) {
				return runToolError("run_start_approval_required", "the one-time approval for this start is missing or was already used")
			}
			accepted.source, accepted.approvalDigest = "approved", digest
			return nil
		}
		if escalates {
			return &contracts.RunToolError{Code: "run_start_approval_required", Retryable: true,
				Message: "accessLevel exceeds the parent Run current level and requires human approval. Call chat_start again with the same target, message and accessLevel"}
		}
		if accessLevel != plan.AccessLevel {
			return &contracts.RunToolError{Code: "run_parent_access_level_changed", Retryable: true,
				Message: "the parent Run permission changed while this start was being admitted. Call chat_start again with the same arguments"}
		}
		if plan.RequestedAccessLevel != "" {
			accepted.source = "explicit"
		}
		return nil
	}
	acceptor, ok := s.deps.Runs.(runAccessLevelAcceptor)
	if !ok {
		return acceptedRunStart{}, runNotStarted(runToolError("run_start_authorization_unavailable", "the Run manager cannot authorize chat_start atomically"))
	}
	active, err := acceptor.AcceptAtRunAccessLevel(strings.TrimSpace(request.Origin.RunID), accept)
	if err != nil {
		return acceptedRunStart{}, runNotStarted(err)
	}
	if !active {
		return acceptedRunStart{}, runNotStarted(runToolError("run_parent_not_active", "the calling Run is no longer active"))
	}
	return accepted, nil
}

// acceptedRunStart is the permission baseline observed at the acceptance point.
type acceptedRunStart struct {
	source              string
	parentAccessLevel   string
	parentAccessVersion int64
	accessLevel         string
	approvalDigest      string
}

func (s *Service) StartRun(ctx context.Context, request contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	plan, err := s.PrepareRunStart(ctx, request)
	if err != nil {
		return contracts.RunSnapshot{}, err
	}
	agentKey := strings.TrimSpace(request.AgentKey)
	teamID := strings.TrimSpace(request.TeamID)
	parentRunID := strings.TrimSpace(request.Origin.RunID)
	req := runtimetypes.QueryCommand{
		ChatID:          strings.TrimSpace(request.ChatID),
		AgentKey:        agentKey,
		TeamID:          teamID,
		Role:            queryinput.QueryRoleUser,
		Message:         strings.TrimSpace(request.Message),
		AccessLevel:     plan.AccessLevel,
		MustUseSkills:   append([]string(nil), request.MustUseSkills...),
		InitialChatName: strings.TrimSpace(request.ChatName),
		ChatSource:      queryinput.ChatSourceRunQueryPrefix + normalizeChatSourcePart(request.Origin.AgentKey),
	}
	if modelKey, effort := strings.TrimSpace(request.ModelKey), strings.TrimSpace(request.ReasoningEffort); modelKey != "" || effort != "" {
		req.Model = &runtimetypes.QueryModelOptions{Key: modelKey, ReasoningEffort: effort}
	}
	// The new Run's lifetime is detached from the caller, but it retains the
	// trusted parent's connection scope. Caller cancellation is still honored
	// until the start is accepted. RunOrigin describes derivation separately
	// from transport/lane.
	runCtx := s.backgroundCtx
	scope, err := s.runControlScopes().Load(parentRunID)
	if err != nil || (scope.Transport != "http" && scope.Transport != "ws") {
		return contracts.RunSnapshot{}, runNotStarted(runToolError("run_control_identity_unavailable", "cannot inherit parent run transport"))
	}
	runCtx = controlscope.WithContext(runCtx, scope)
	runCtx = runtimetypes.WithChatSource(runCtx, req.ChatSource)
	if subject := strings.TrimSpace(request.Origin.Subject); subject != "" {
		runCtx = runtimetypes.WithIdentity(runCtx, &contracts.AuthIdentity{Subject: subject})
	}
	locale, err := s.deps.Sessions.RunPromptLocale(parentRunID)
	if err != nil {
		return contracts.RunSnapshot{}, runNotStarted(err)
	}
	// Target admission runs before the approval is spent, so a rejected target
	// never burns a human approval. It creates no Chat.
	admission, err := s.PrepareQueryAdmissionRequest(runCtx, req, true, locale, "")
	if err != nil {
		return contracts.RunSnapshot{}, runNotStarted(mapRunAdmissionError(err, agentKey, teamID))
	}
	admission.StrictOwner = true
	accepted, err := s.acceptRunStart(ctx, request, plan)
	if err != nil {
		releaseQuery(admission.Release)
		return contracts.RunSnapshot{}, err
	}
	// Accepted: from here the approval is spent and is never restored.
	prepared, err := s.CompleteQueryPreparation(runCtx, admission, nil)
	if err != nil {
		return contracts.RunSnapshot{}, runNotStarted(mapRunAdmissionError(err, agentKey, teamID))
	}
	origin := request.Origin
	prepared.Session.RunOrigin = &origin
	permission := map[string]any{
		"parentAccessLevel":   accepted.parentAccessLevel,
		"parentAccessVersion": accepted.parentAccessVersion,
		"accessLevel":         accepted.accessLevel,
		"source":              accepted.source,
		"requestDigest":       plan.RequestDigest,
	}
	if plan.RequestedAccessLevel != "" {
		permission["requestedAccessLevel"] = plan.RequestedAccessLevel
	}
	if accepted.approvalDigest != "" {
		permission["approvalDigest"] = accepted.approvalDigest
	}
	auditMetadata := map[string]any{
		"runOrigin": map[string]any{
			"agentKey":   strings.TrimSpace(origin.AgentKey),
			"chatId":     strings.TrimSpace(origin.ChatID),
			"runId":      strings.TrimSpace(origin.RunID),
			"toolId":     strings.TrimSpace(origin.ToolID),
			"permission": permission,
		},
	}
	prepared.Req.TrustedQueryMetadata = contracts.CloneMap(auditMetadata)
	prepared.Execution = &queryExecutionOptions{
		StepLineStore:   s.deps.Chats,
		CompletionStore: s.deps.Chats,
		QueryMetadata:   auditMetadata,
	}

	registered, statusErr := s.RegisterPreparedQuery(runCtx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		return contracts.RunSnapshot{}, runNotStarted(mapRunStatusError(statusErr))
	}
	eventBus, ok := s.deps.Runs.EventBus(prepared.Req.RunID)
	if !ok {
		releaseQuery(prepared.Release)
		s.deps.Runs.Interrupt(serverSetupInterruptRequest(prepared.Req, contracts.InterruptReasonEventBusUnavailable, "run event bus unavailable"))
		s.FinishRegisteredQuery(prepared, registered)
		return contracts.RunSnapshot{}, runNotStarted(runToolError("internal_error", "run event bus unavailable"))
	}

	if sessionbuild.IsProxyRoutedAgent(prepared.AgentDef) {
		s.deps.Proxy.Start(prepared, registered, eventBus, false)
	} else {
		s.startPreparedLocalRun(prepared, registered, eventBus)
	}
	snapshot, err := s.GetRunStatus(prepared.Req.RunID)
	if err != nil {
		// The Run was handed to its executor; only its status read failed.
		return contracts.RunSnapshot{}, &contracts.RunToolError{
			Code: "run_start_outcome_unknown", ExecutionState: "unknown",
			RunID: prepared.Req.RunID, ChatID: prepared.Req.ChatID,
			Message: "the Run was started but its status could not be confirmed; check it with chat_get_status before starting anything again",
		}
	}
	return snapshot, nil
}

// runNotStarted marks a failure that happened before any Run was started.
func runNotStarted(err error) error {
	if err == nil {
		return nil
	}
	var typed *contracts.RunToolError
	if !errors.As(err, &typed) {
		return &contracts.RunToolError{Code: "internal_error", Message: err.Error(), ExecutionState: "not_started"}
	}
	if typed.ExecutionState == "" {
		typed.ExecutionState = "not_started"
	}
	return typed
}

func mapRunAdmissionError(err error, agentKey string, teamID string) error {
	var statusErr *statusError
	if !errors.As(err, &statusErr) {
		return err
	}
	if strings.Contains(strings.ToLower(statusErr.Message), "agent not found") && agentKey != "" {
		return runToolError("agent_not_found", statusErr.Message)
	}
	if strings.Contains(strings.ToLower(statusErr.Message), "team") && strings.Contains(strings.ToLower(statusErr.Message), "not found") && teamID != "" {
		return runToolError("team_not_found", statusErr.Message)
	}
	return mapRunStatusError(statusErr)
}

func runToolError(code string, message string) error {
	return &contracts.RunToolError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}
