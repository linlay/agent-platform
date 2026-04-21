package server

import (
	"net/http"
	"strconv"
	"strings"

	"agent-platform-runner-go/internal/api"
)

func (s *Server) handleSkillCandidates(w http.ResponseWriter, r *http.Request) {
	if s.deps.SkillCandidates == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "skill candidate store is not configured"))
		return
	}
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	items, err := s.deps.SkillCandidates.List(agentKey, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(items))
}
