package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
)

func (s *Server) handleGlobalSearch(w http.ResponseWriter, r *http.Request) {
	var req api.GlobalSearchRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Query) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "query is required"))
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	hits, err := s.deps.Chats.SearchGlobal(req.Query, req.AgentKey, req.TeamID, limit)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	results := make([]api.GlobalSearchResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, api.GlobalSearchResult{
			ChatID:    hit.ChatID,
			ChatName:  hit.ChatName,
			AgentKey:  hit.AgentKey,
			TeamID:    hit.TeamID,
			RunID:     hit.RunID,
			Kind:      hit.Kind,
			Role:      hit.Role,
			Timestamp: hit.Timestamp,
			Snippet:   hit.Snippet,
			Score:     hit.Score,
		})
	}
	writeJSON(w, http.StatusOK, api.Success(api.GlobalSearchResponse{
		Query:   strings.TrimSpace(req.Query),
		Count:   len(results),
		Results: results,
	}))
}
