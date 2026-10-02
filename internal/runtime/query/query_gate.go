package query

import (
	"context"
	"errors"
	"strings"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/controlscope"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/timecontract"
)

const (
	awaitingPendingCode    = "awaiting_pending"
	awaitingPendingMessage = "pending awaiting found for chat"
)

func isContinuableDeferredAwaitingMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question", "planning", "wait":
		return true
	default:
		return false
	}
}

func (s *Service) FinishRegisteredQuery(prepared preparedQuery, registered registeredQueryRun) {
	if s == nil || s.deps.Runs == nil || !registered.Managed {
		return
	}
	s.deps.Runs.Finish(prepared.Req.RunID)
}

func (s *Service) clearPendingAwaitingGate(chatID string, awaitingID string) {
	if s == nil {
		return
	}
	if s.deps.Chats != nil {
		_ = s.deps.Chats.ClearPendingAwaiting(chatID, awaitingID)
	}
	if s.deferredAwaitings != nil {
		s.deferredAwaitings.Remove(awaitingID)
	}
}

func isAwaitingGateMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question", "planning", "form", "approval", "wait":
		return true
	default:
		return false
	}
}

func (s *Service) AwaitingQueryGateError(chatID string, summary *chat.Summary) *statusError {
	if s == nil || s.deps.Chats == nil || summary == nil || summary.PendingAwaiting == nil {
		return nil
	}
	info, err := s.PendingAwaitingInfo(chatID, summary.PendingAwaiting)
	if err != nil {
		return &statusError{Status: 500, Code: "internal_error", Message: err.Error()}
	}
	if info == nil {
		return nil
	}
	return &statusError{
		Status:  409,
		Code:    awaitingPendingCode,
		Message: awaitingPendingMessage,
		Data:    info,
	}
}

func (s *Service) PendingAwaitingInfo(chatID string, pending *chat.PendingAwaiting) (*queryinput.ChatErrorInfo, error) {
	if pending == nil {
		return nil, nil
	}
	chatID = strings.TrimSpace(chatID)
	awaitingID := strings.TrimSpace(pending.AwaitingID)
	if chatID == "" || awaitingID == "" {
		return nil, nil
	}
	pendingInfo := func(runID, mode string) *queryinput.ChatErrorInfo {
		return awaitingPendingInfo(chatID, queryinput.Awaiting{AwaitingID: awaitingID, RunID: runID, Mode: mode, Status: "awaiting", CreatedAt: pending.CreatedAt})
	}
	if contracts.AwaitingHasLiveExecutor(s.deps.Runs, pending.RunID, awaitingID) {
		return pendingInfo(pending.RunID, pending.Mode), nil
	}
	pendingMode := strings.ToLower(strings.TrimSpace(pending.Mode))
	if !isAwaitingGateMode(pendingMode) {
		s.clearPendingAwaitingGate(chatID, awaitingID)
		return nil, nil
	}
	ask, err := s.deps.Chats.LoadAwaitingAsk(chatID, awaitingID)
	if err != nil {
		return nil, err
	}
	if ask == nil {
		s.clearPendingAwaitingGate(chatID, awaitingID)
		return nil, nil
	}
	if ask.Payload == nil {
		ask.Payload = map[string]any{}
	}
	effectiveMode := strings.ToLower(firstNonBlank(pending.Mode, ask.Mode, sessionbuild.StringValue(ask.Payload["mode"])))
	if !isAwaitingGateMode(effectiveMode) {
		s.clearPendingAwaitingGate(chatID, awaitingID)
		return nil, nil
	}
	runID := firstNonBlank(pending.RunID, ask.RunID, sessionbuild.StringValue(ask.Payload["runId"]))
	if contracts.AwaitingHasLiveExecutor(s.deps.Runs, runID, awaitingID) {
		return pendingInfo(runID, effectiveMode), nil
	}
	timeoutSec := contracts.AnyIntNode(ask.Payload["timeout"])
	if awaitingTimeoutApplies(effectiveMode) && timeoutSec > 0 && time.Now().UnixMilli()-pending.CreatedAt > int64(timeoutSec)*1000 {
		resolvedAt := time.Now().UnixMilli()
		answer := contracts.AwaitingTimeoutAnswer(effectiveMode, int64(timeoutSec), maxInt64((resolvedAt-pending.CreatedAt)/1000, int64(timeoutSec)))
		item := chat.PendingAwaitingWithChat{
			ChatID:     chatID,
			AwaitingID: awaitingID,
			RunID:      firstNonBlank(pending.RunID, ask.RunID, sessionbuild.StringValue(ask.Payload["runId"])),
			Mode:       effectiveMode,
			CreatedAt:  pending.CreatedAt,
		}
		state, err := s.FinishTerminalAwaiting(item, answer, resolvedAt)
		if err != nil {
			return nil, err
		}
		if state == contracts.AwaitingResolutionOwned {
			return pendingInfo(runID, effectiveMode), nil
		}
		return nil, nil
	}
	return awaitingPendingInfo(chatID, queryinput.Awaiting{
		AwaitingID: awaitingID,
		RunID:      firstNonBlank(pending.RunID, ask.RunID, sessionbuild.StringValue(ask.Payload["runId"])),
		Mode:       effectiveMode,
		Status:     "awaiting",
		CreatedAt:  pending.CreatedAt,
	}), nil
}

func awaitingTimeoutApplies(mode string) bool {
	return !strings.EqualFold(strings.TrimSpace(mode), "planning")
}

type registeredQueryRun = runtimetypes.RegisteredRun

func (s *Service) registeredQueryRun(observerCtx context.Context, runCtx context.Context, control *contracts.RunControl, prepared preparedQuery) (registeredQueryRun, *statusError) {
	runID := prepared.Req.RunID
	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		s.deps.Runs.Finish(runID)
		return registeredQueryRun{}, &statusError{Status: 500, Code: "internal_error", Message: "registered run status is unavailable"}
	}
	if err := timecontract.ValidateEpochMillis(status.StartedAt, "startedAt", "run.registration"); err != nil {
		s.deps.Runs.Finish(runID)
		return registeredQueryRun{}, &statusError{
			Status:  422,
			Code:    "time_contract_violation",
			Message: err.Error(),
			Data:    timecontract.ErrorData(err),
		}
	}
	execution := s.resolvedQueryExecution(prepared)
	if !execution.HiddenRun {
		start := chat.RunStart{
			ChatID:          prepared.Req.ChatID,
			RunID:           runID,
			AgentKey:        prepared.Req.AgentKey,
			AgentMode:       chatAgentMode(prepared.AgentDef, contracts.IsTeamRunOwner(prepared.Req.AgentKey, prepared.Req.TeamID)),
			TeamID:          prepared.Req.TeamID,
			InitialMessage:  prepared.Req.Message,
			StartedAtMillis: status.StartedAt,
		}
		recorder, ok := execution.CompletionStore.(chat.RunStartRecorder)
		if !ok || recorder == nil {
			s.deps.Runs.Finish(runID)
			return registeredQueryRun{}, &statusError{Status: 500, Code: "internal_error", Message: "run start recorder is not configured"}
		}
		if err := recorder.OnRunStarted(start); err != nil {
			s.deps.Runs.Finish(runID)
			if isTimeContractViolation(err) {
				return registeredQueryRun{}, &statusError{Status: 422, Code: "time_contract_violation", Message: timeContractViolationMessage, Data: timeContractErrorData(err)}
			}
			return registeredQueryRun{}, &statusError{Status: 500, Code: "internal_error", Message: err.Error()}
		}
		notifyInternalQueryRunStarted(observerCtx, start)
	}
	if err := s.FreezeRunConnectors(prepared); err != nil {
		s.deps.Runs.Finish(runID)
		return registeredQueryRun{}, btwStatusError(500, "run_connector_snapshot_unavailable", err.Error())
	}
	return registeredQueryRun{RunCtx: runCtx, Control: control, Managed: true, StartedAtMillis: status.StartedAt}, nil
}

func (s *Service) cleanupUnregisteredRunEnvironment(session contracts.QuerySession) {
	state := session.RunEnvironment
	if state == nil {
		return
	}
	if existing, ok := sessionbuild.LookupRunEnvironment(s.deps.Runs, session.RunID); ok && existing == state {
		return
	}
	state.Destroy()
}

func awaitingPendingInfo(chatID string, awaiting queryinput.Awaiting) *queryinput.ChatErrorInfo {
	return &queryinput.ChatErrorInfo{
		Code:     awaitingPendingCode,
		Message:  awaitingPendingMessage,
		ChatID:   strings.TrimSpace(chatID),
		Awaiting: &awaiting,
	}
}

func (s *Service) RegisterPreparedQuery(ctx context.Context, prepared preparedQuery) (registeredQueryRun, *statusError) {
	defer s.cleanupUnregisteredRunEnvironment(prepared.Session)
	if s == nil || s.deps.Runs == nil {
		return registeredQueryRun{}, &statusError{Status: 500, Code: "internal_error", Message: "run manager is not configured"}
	}
	if err := s.runControlScopes().Bind(prepared.Req.RunID, controlscope.FromContext(ctx)); err != nil {
		if errors.Is(err, controlscope.ErrConflict) {
			return registeredQueryRun{}, btwStatusError(409, "run_control_identity_conflict", err.Error())
		}
		return registeredQueryRun{}, btwStatusError(500, "run_control_identity_unavailable", "cannot persist run control identity")
	}
	if prepared.Session.InteractionConfig != nil {
		if err := s.RunInteractionPolicies().Bind(prepared.Req.RunID, *prepared.Session.InteractionConfig); err != nil {
			return registeredQueryRun{}, btwStatusError(500, "run_interaction_policy_unavailable", "cannot persist run interaction policy")
		}
	}
	if registrar, ok := s.deps.Runs.(contracts.ExclusiveRunRegistrar); ok {
		registration, err := registrar.RegisterExclusiveForChat(ctx, prepared.Session)
		if err != nil {
			var conflictErr *contracts.ActiveRunConflictError
			if errors.As(err, &conflictErr) {
				return registeredQueryRun{}, &statusError{
					Status:  409,
					Code:    activeRunConflictCode,
					Message: activeRunConflictMessage,
					Data:    activeRunConflictInfo(conflictErr),
				}
			}
			return registeredQueryRun{}, &statusError{Status: 500, Code: "internal_error", Message: err.Error()}
		}
		if !registration.Registered {
			active := registration.ActiveRun
			chatID := firstNonBlank(active.ChatID, prepared.Req.ChatID)
			runID := strings.TrimSpace(active.RunID)
			runIDs := []string{}
			if runID != "" {
				runIDs = append(runIDs, runID)
			}
			return registeredQueryRun{}, &statusError{
				Status:  409,
				Code:    activeRunConflictCode,
				Message: activeRunFoundMessage,
				Data:    activeRunFoundInfo(chatID, runIDs),
			}
		}
		return s.registeredQueryRun(ctx, registration.Context, registration.Control, prepared)
	}

	runScopeID := strings.TrimSpace(prepared.Session.RunScopeID)
	if runScopeID == "" {
		runScopeID = prepared.Req.ChatID
	}
	activeRun, ok, activeErr := s.deps.Runs.ActiveRunForChat(runScopeID)
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(activeErr, &conflictErr) {
		return registeredQueryRun{}, &statusError{
			Status:  409,
			Code:    activeRunConflictCode,
			Message: activeRunConflictMessage,
			Data:    activeRunConflictInfo(conflictErr),
		}
	}
	if activeErr != nil {
		return registeredQueryRun{}, &statusError{Status: 500, Code: "internal_error", Message: activeErr.Error()}
	}
	if ok {
		return registeredQueryRun{}, &statusError{
			Status:  409,
			Code:    activeRunConflictCode,
			Message: activeRunFoundMessage,
			Data:    activeRunFoundInfo(prepared.Req.ChatID, []string{activeRun.RunID}),
		}
	}
	runCtx, control, _ := s.deps.Runs.Register(ctx, prepared.Session)
	return s.registeredQueryRun(ctx, runCtx, control, prepared)
}
