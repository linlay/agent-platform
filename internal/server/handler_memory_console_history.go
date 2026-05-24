package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/memory"
)

func (s *Server) handleMemoryHistory(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.deps.Memory.(memory.HistoryProvider)
	if !s.memorySystemEnabled() || s.deps.Memory == nil || !ok {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory history is not configured"))
		return
	}
	limit, ok := parseMemoryLimit(w, r, 50)
	if !ok {
		return
	}
	result, err := provider.History(memory.HistoryFilter{
		AgentKey:  strings.TrimSpace(r.URL.Query().Get("agentKey")),
		ChatID:    strings.TrimSpace(r.URL.Query().Get("chatId")),
		RunID:     strings.TrimSpace(r.URL.Query().Get("runId")),
		MemoryID:  firstQueryValue(r, "memoryId", "id"),
		Operation: strings.TrimSpace(r.URL.Query().Get("operation")),
		Limit:     limit,
		Cursor:    strings.TrimSpace(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	events := toMemoryHistoryEvents(result.Events)
	writeJSON(w, http.StatusOK, api.Success(api.MemoryHistoryResponse{
		Count:      len(events),
		NextCursor: result.NextCursor,
		Events:     events,
	}))
}
