package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func (s *Server) handleSessionSearch(w http.ResponseWriter, r *http.Request) {
	var req api.SessionSearchRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ChatID) == "" || strings.TrimSpace(req.Query) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId and query are required"))
		return
	}
	if s.deps.Chats == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "chat store is not configured"))
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	hits, err := s.deps.Chats.SearchSession(req.ChatID, req.Query, limit)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	results := make([]api.SessionSearchResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, api.SessionSearchResult{
			Kind:      hit.Kind,
			ChatID:    hit.ChatID,
			RunID:     hit.RunID,
			Stage:     hit.Stage,
			Role:      hit.Role,
			Timestamp: hit.Timestamp,
			Snippet:   hit.Snippet,
			Score:     hit.Score,
			Meta:      hit.Meta,
		})
	}
	writeJSON(w, http.StatusOK, api.Success(api.SessionSearchResponse{
		ChatID:  req.ChatID,
		Query:   strings.TrimSpace(req.Query),
		Count:   len(results),
		Results: results,
	}))
}
