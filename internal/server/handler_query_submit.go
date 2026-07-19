package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
)

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid submit payload"))
		return
	}
	req.Locale = requestLocale(r, responseLocale(w))
	req = s.normalizeActiveSubmitRun(req)
	if statusErr := s.validateSubmitOwner(req); statusErr != nil {
		writeStatusError(w, statusErr)
		return
	}
	if response, statusErr, ok := s.forwardProxySubmit(req); ok {
		if statusErr != nil {
			writeStatusError(w, statusErr)
			return
		}
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	response, _, _, err := s.resolveSubmit(req)
	if err != nil {
		if statusErr, ok := err.(*statusError); ok {
			writeStatusError(w, statusErr)
			return
		}
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleSteer(w http.ResponseWriter, r *http.Request) {
	var req api.SteerRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" || strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId and message are required"))
		return
	}
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		writeStatusError(w, statusErr)
		return
	}
	if response, statusErr, ok := s.forwardProxySteer(req); ok {
		if statusErr != nil {
			writeStatusError(w, statusErr)
			return
		}
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	ack := s.deps.Runs.Steer(req)
	writeJSON(w, http.StatusOK, api.Success(api.SteerResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		SteerID:  ack.SteerID,
		Detail:   ack.Detail,
	}))
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	var req api.InterruptRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId is required"))
		return
	}
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		writeStatusError(w, statusErr)
		return
	}
	if response, statusErr, ok := s.forwardProxyInterrupt(req); ok {
		if statusErr != nil {
			writeStatusError(w, statusErr)
			return
		}
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	ack := s.deps.Runs.Interrupt(httpAPIUserInterruptRequest(req))
	writeJSON(w, http.StatusOK, api.Success(api.InterruptResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		Detail:   ack.Detail,
	}))
}
