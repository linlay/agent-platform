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
	if statusErr := s.validateSubmitAgentKey(req); statusErr != nil {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
		return
	}
	if response, statusErr, ok := s.forwardProxySubmit(req); ok {
		if statusErr != nil {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	response, _, _, err := s.resolveSubmit(req)
	if err != nil {
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
	if statusErr := s.validateRunAgentKey(req.RunID, req.AgentKey); statusErr != nil {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
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
	if statusErr := s.validateRunAgentKey(req.RunID, req.AgentKey); statusErr != nil {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
		return
	}
	if response, statusErr, ok := s.forwardProxyInterrupt(req); ok {
		if statusErr != nil {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	ack := s.deps.Runs.Interrupt(req)
	writeJSON(w, http.StatusOK, api.Success(api.InterruptResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		Detail:   ack.Detail,
	}))
}
