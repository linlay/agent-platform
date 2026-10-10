package server

import (
	"net/http"

	"agent-platform/internal/api"
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
		RunRef:    runtimetypes.RunRef{RunID: req.RunID, AgentKey: req.AgentKey},
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
