package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
)

const (
	awaitingPendingCode    = "awaiting_pending"
	awaitingPendingMessage = "pending awaiting found for chat"
)

type registeredQueryRun struct {
	RunCtx          context.Context
	Control         *contracts.RunControl
	Managed         bool
	StartedAtMillis int64
}

func writeStatusError(w http.ResponseWriter, err *statusError) {
	if err == nil {
		return
	}
	if err.data != nil {
		msg := strings.TrimSpace(err.code)
		if msg == "" {
			msg = err.message
		}
		writeJSON(w, err.status, api.ApiResponse[any]{
			Code: err.status,
			Msg:  msg,
			Data: err.data,
		})
		return
	}
	writeJSON(w, err.status, api.Failure(err.status, err.message))
}

func (s *Server) awaitingQueryGateError(chatID string, summary *chat.Summary) *statusError {
	if s == nil || s.deps.Chats == nil || summary == nil || summary.PendingAwaiting == nil {
		return nil
	}
	info, err := s.validPendingAwaitingInfo(chatID, summary.PendingAwaiting)
	if err != nil {
		return &statusError{status: http.StatusInternalServerError, code: "internal_error", message: err.Error()}
	}
	if info == nil {
		return nil
	}
	return &statusError{
		status:  http.StatusConflict,
		code:    awaitingPendingCode,
		message: awaitingPendingMessage,
		data:    info,
	}
}

func (s *Server) validPendingAwaitingInfo(chatID string, pending *chat.PendingAwaiting) (*api.ChatErrorInfo, error) {
	if pending == nil {
		return nil, nil
	}
	chatID = strings.TrimSpace(chatID)
	awaitingID := strings.TrimSpace(pending.AwaitingID)
	if chatID == "" || awaitingID == "" {
		return nil, nil
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
	effectiveMode := strings.ToLower(firstNonBlank(pending.Mode, ask.Mode, stringValue(ask.Payload["mode"])))
	if !isAwaitingGateMode(effectiveMode) {
		s.clearPendingAwaitingGate(chatID, awaitingID)
		return nil, nil
	}
	timeoutSec := contracts.AnyIntNode(ask.Payload["timeout"])
	if timeoutSec > 0 && time.Now().UnixMilli()-pending.CreatedAt > int64(timeoutSec)*1000 {
		s.clearPendingAwaitingGate(chatID, awaitingID)
		return nil, nil
	}
	return awaitingPendingInfo(chatID, api.Awaiting{
		AwaitingID: awaitingID,
		RunID:      firstNonBlank(pending.RunID, ask.RunID, stringValue(ask.Payload["runId"])),
		Mode:       effectiveMode,
		Status:     "awaiting",
		CreatedAt:  pending.CreatedAt,
	}), nil
}

func (s *Server) clearPendingAwaitingGate(chatID string, awaitingID string) {
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

func awaitingPendingInfo(chatID string, awaiting api.Awaiting) *api.ChatErrorInfo {
	return &api.ChatErrorInfo{
		Code:     awaitingPendingCode,
		Message:  awaitingPendingMessage,
		ChatID:   strings.TrimSpace(chatID),
		Awaiting: &awaiting,
	}
}

func (s *Server) registerQueryRun(ctx context.Context, prepared preparedQuery) (registeredQueryRun, *statusError) {
	if s == nil || s.deps.Runs == nil {
		return registeredQueryRun{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: "run manager is not configured"}
	}
	if registrar, ok := s.deps.Runs.(contracts.ExclusiveRunRegistrar); ok {
		registration, err := registrar.RegisterExclusiveForChat(ctx, prepared.session)
		if err != nil {
			var conflictErr *contracts.ActiveRunConflictError
			if errors.As(err, &conflictErr) {
				return registeredQueryRun{}, &statusError{
					status:  http.StatusConflict,
					code:    activeRunConflictCode,
					message: activeRunConflictMessage,
					data:    activeRunConflictInfo(conflictErr),
				}
			}
			return registeredQueryRun{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: err.Error()}
		}
		if !registration.Registered {
			active := registration.ActiveRun
			chatID := firstNonBlank(active.ChatID, prepared.req.ChatID)
			runID := strings.TrimSpace(active.RunID)
			runIDs := []string{}
			if runID != "" {
				runIDs = append(runIDs, runID)
			}
			return registeredQueryRun{}, &statusError{
				status:  http.StatusConflict,
				code:    activeRunConflictCode,
				message: activeRunFoundMessage,
				data:    activeRunFoundInfo(chatID, runIDs),
			}
		}
		return s.registeredQueryRun(registration.Context, registration.Control, prepared)
	}

	runScopeID := strings.TrimSpace(prepared.session.RunScopeID)
	if runScopeID == "" {
		runScopeID = prepared.req.ChatID
	}
	activeRun, ok, activeErr := s.deps.Runs.ActiveRunForChat(runScopeID)
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(activeErr, &conflictErr) {
		return registeredQueryRun{}, &statusError{
			status:  http.StatusConflict,
			code:    activeRunConflictCode,
			message: activeRunConflictMessage,
			data:    activeRunConflictInfo(conflictErr),
		}
	}
	if activeErr != nil {
		return registeredQueryRun{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: activeErr.Error()}
	}
	if ok {
		return registeredQueryRun{}, &statusError{
			status:  http.StatusConflict,
			code:    activeRunConflictCode,
			message: activeRunFoundMessage,
			data:    activeRunFoundInfo(prepared.req.ChatID, []string{activeRun.RunID}),
		}
	}
	runCtx, control, _ := s.deps.Runs.Register(ctx, prepared.session)
	return s.registeredQueryRun(runCtx, control, prepared)
}

// registeredQueryRun reads the single authoritative lifecycle timestamp from
// the run manager immediately after registration.  Every subsequent
// persistence and push path receives this same value; never infer it from a
// run ID or a later completion timestamp.

func (s *Server) registeredQueryRun(runCtx context.Context, control *contracts.RunControl, prepared preparedQuery) (registeredQueryRun, *statusError) {
	runID := prepared.req.RunID
	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		s.deps.Runs.Finish(runID)
		return registeredQueryRun{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: "registered run status is unavailable"}
	}
	if err := timecontract.ValidateEpochMillis(status.StartedAt, "startedAt", "run.registration"); err != nil {
		s.deps.Runs.Finish(runID)
		return registeredQueryRun{}, &statusError{
			status:  http.StatusUnprocessableEntity,
			code:    "time_contract_violation",
			message: err.Error(),
			data:    timecontract.ErrorData(err),
		}
	}
	execution := s.resolvedQueryExecution(prepared)
	if !execution.HiddenRun {
		recorder, ok := execution.CompletionStore.(chat.RunStartRecorder)
		if !ok || recorder == nil {
			s.deps.Runs.Finish(runID)
			return registeredQueryRun{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: "run start recorder is not configured"}
		}
		if err := recorder.OnRunStarted(chat.RunStart{
			ChatID:          prepared.req.ChatID,
			RunID:           runID,
			AgentKey:        prepared.req.AgentKey,
			AgentMode:       chatAgentMode(prepared.agentDef, contracts.IsTeamRunOwner(prepared.req.AgentKey, prepared.req.TeamID)),
			TeamID:          prepared.req.TeamID,
			InitialMessage:  prepared.req.Message,
			StartedAtMillis: status.StartedAt,
		}); err != nil {
			s.deps.Runs.Finish(runID)
			if isTimeContractViolation(err) {
				return registeredQueryRun{}, &statusError{status: http.StatusUnprocessableEntity, code: "time_contract_violation", message: timeContractViolationMessage, data: timeContractErrorData(err)}
			}
			return registeredQueryRun{}, &statusError{status: http.StatusInternalServerError, code: "internal_error", message: err.Error()}
		}
	}
	return registeredQueryRun{RunCtx: runCtx, Control: control, Managed: true, StartedAtMillis: status.StartedAt}, nil
}

func (s *Server) finishRegisteredQueryRun(prepared preparedQuery, registered registeredQueryRun) {
	if s == nil || s.deps.Runs == nil || !registered.Managed {
		return
	}
	s.deps.Runs.Finish(prepared.req.RunID)
}

func isAwaitingGateMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question", "planning", "form", "approval":
		return true
	default:
		return false
	}
}

func isContinuableDeferredAwaitingMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question", "planning":
		return true
	default:
		return false
	}
}
