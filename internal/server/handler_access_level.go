package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func (s *Server) handleAccessLevel(w http.ResponseWriter, r *http.Request) {
	var req api.AccessLevelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid access-level payload"))
		return
	}
	response, statusErr := s.updateAccessLevel(req)
	if statusErr != nil {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) updateAccessLevel(req api.AccessLevelRequest) (api.AccessLevelResponse, *statusError) {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AgentKey) == "" {
		return api.AccessLevelResponse{}, &statusError{status: http.StatusBadRequest, message: "runId and agentKey are required"}
	}
	accessLevel, ok := contracts.NormalizeAccessLevel(req.AccessLevel)
	if !ok {
		return api.AccessLevelResponse{}, &statusError{status: http.StatusBadRequest, message: "accessLevel must be default, auto_approve, or full_access"}
	}
	req.AccessLevel = accessLevel
	if statusErr := s.validateRunAgentKey(req.RunID, req.AgentKey); statusErr != nil {
		return api.AccessLevelResponse{}, statusErr
	}
	if response, statusErr, ok := s.forwardProxyAccessLevel(req); ok {
		return response, statusErr
	}
	ack := s.deps.Runs.UpdateAccessLevel(req)
	return api.AccessLevelResponse{
		Accepted:            ack.Accepted,
		Status:              ack.Status,
		RunID:               req.RunID,
		PreviousAccessLevel: ack.PreviousAccessLevel,
		AccessLevel:         ack.AccessLevel,
		Version:             ack.Version,
		Detail:              ack.Detail,
	}, nil
}
