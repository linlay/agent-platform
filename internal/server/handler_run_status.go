package server

import (
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
)

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId is required"))
		return
	}
	status, ok := s.deps.Runs.RunStatus(runID)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "run not found"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.RunStatusResponse{
		RunID:         status.RunID,
		ChatID:        status.ChatID,
		AgentKey:      status.AgentKey,
		State:         string(status.State),
		LastSeq:       status.LastSeq,
		OldestSeq:     status.OldestSeq,
		ObserverCount: status.ObserverCount,
		StartedAt:     status.StartedAt,
		CompletedAt:   status.CompletedAt,
	}))
}
