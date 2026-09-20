package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/interaction"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Server) handleAccessLevel(w http.ResponseWriter, r *http.Request) {
	var req api.AccessLevelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid access-level payload"))
		return
	}
	if !s.validateHTTPRunControl(w, r, req.RunID) {
		return
	}
	result, err := s.deps.Runtime.SetAccessLevel(r.Context(), runtimetypes.AccessLevelCommand{
		RunRef:    runtimetypes.RunRef{RunID: req.RunID, AgentKey: req.AgentKey, TeamID: req.TeamID},
		RequestID: req.RequestID, AccessLevel: req.AccessLevel, Reason: req.Reason,
	})
	if err != nil {
		writeRuntimeControlError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.AccessLevelResponse{
		Accepted: result.Accepted, Status: result.Status, RunID: result.RunID,
		PreviousAccessLevel: result.PreviousAccessLevel, AccessLevel: result.AccessLevel,
		Version: result.Version, Detail: result.Detail,
	}))
}

func (s *Server) updateAccessLevel(req api.AccessLevelRequest) (api.AccessLevelResponse, *statusError) {
	if strings.TrimSpace(req.RunID) == "" {
		return api.AccessLevelResponse{}, &statusError{status: http.StatusBadRequest, message: "runId is required"}
	}
	accessLevel, ok := contracts.NormalizeAccessLevel(req.AccessLevel)
	if !ok {
		return api.AccessLevelResponse{}, &statusError{status: http.StatusBadRequest, message: "accessLevel must be default, auto_approve, or full_access"}
	}
	req.AccessLevel = accessLevel
	if statusErr := s.validateRunOwner(req.RunID, req.AgentKey, req.TeamID); statusErr != nil {
		return api.AccessLevelResponse{}, statusErr
	}
	// Validate before forwarding: ACP must never receive a disallowed change.
	if reader, ok := s.deps.Runs.(interface {
		RunInteractionConfig(string) (interaction.Config, bool)
	}); ok {
		if config, found := reader.RunInteractionConfig(req.RunID); found && !config.AccessLevel {
			return api.AccessLevelResponse{RunID: req.RunID, Status: "interaction_disabled", Detail: "interactionConfig.accessLevel is disabled"}, nil
		}
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
