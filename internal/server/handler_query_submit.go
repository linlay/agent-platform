package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid submit payload"))
		return
	}
	req.Locale = requestLocale(r, responseLocale(w))
	result, err := s.deps.Runtime.Submit(r.Context(), runtimeSubmitCommand(req))
	if err != nil {
		writeRuntimeControlError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.SubmitResponse{
		Accepted: result.Accepted, Status: result.Status, ChatID: result.ChatID, RunID: result.RunID,
		AwaitingID: result.AwaitingID, SubmitID: result.SubmitID, Continued: result.Continued,
		ErrorCode: result.ErrorCode, Detail: result.Detail,
	}))
}

func (s *Server) handleSteer(w http.ResponseWriter, r *http.Request) {
	var req api.SteerRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" || strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId and message are required"))
		return
	}
	result, err := s.deps.Runtime.Steer(r.Context(), runtimeSteerCommand(req))
	if err != nil {
		writeRuntimeControlError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.SteerResponse{
		Accepted: result.Accepted, Status: result.Status, RunID: result.RunID,
		SteerID: result.SteerID, Detail: result.Detail,
	}))
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	var req api.InterruptRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId is required"))
		return
	}
	result, err := s.deps.Runtime.Interrupt(r.Context(), runtimeInterruptCommand(req))
	if err != nil {
		writeRuntimeControlError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.InterruptResponse{
		Accepted: result.Accepted, Status: result.Status, RunID: result.RunID, Detail: result.Detail,
	}))
}

func runtimeSubmitCommand(req api.SubmitRequest) runtimetypes.SubmitCommand {
	var params []json.RawMessage
	if req.Params != nil {
		params = make([]json.RawMessage, len(req.Params))
		copy(params, req.Params)
	}
	return runtimetypes.SubmitCommand{
		RunRef:     runtimetypes.RunRef{RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey, TeamID: req.TeamID},
		AwaitingID: req.AwaitingID, SubmitID: req.SubmitID, Locale: req.Locale,
		Params: params, ContinuationRunID: req.ContinuationRunID, ContinuationState: req.ContinuationState,
	}
}

func runtimeSteerCommand(req api.SteerRequest) runtimetypes.SteerCommand {
	return runtimetypes.SteerCommand{
		RunRef:    runtimetypes.RunRef{RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey, TeamID: req.TeamID},
		RequestID: req.RequestID, SteerID: req.SteerID, Message: req.Message,
	}
}

func runtimeInterruptCommand(req api.InterruptRequest) runtimetypes.InterruptCommand {
	return runtimetypes.InterruptCommand{
		RunRef:    runtimetypes.RunRef{RunID: req.RunID, ChatID: req.ChatID, AgentKey: req.AgentKey, TeamID: req.TeamID},
		RequestID: req.RequestID, Message: req.Message, Source: req.InterruptSource,
		Reason: req.InterruptReason, Detail: req.InterruptDetail,
	}
}

func writeRuntimeControlError(w http.ResponseWriter, err error, fallbackStatus int) {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		writeStatusError(w, statusErr)
		return
	}
	status := fallbackStatus
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		status = apperrorsStatus(appErr, fallbackStatus)
	}
	writeJSON(w, status, api.Failure(status, err.Error()))
}

func apperrorsStatus(err *apperrors.Error, fallback int) int {
	if err == nil {
		return fallback
	}
	if status := contracts.AnyIntNode(err.Payload()["status"]); status > 0 {
		return status
	}
	return fallback
}
