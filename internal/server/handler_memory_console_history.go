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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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

func toMemoryHistoryEvents(events []memory.HistoryEvent) []api.MemoryHistoryEvent {
	if len(events) == 0 {
		return []api.MemoryHistoryEvent{}
	}
	out := make([]api.MemoryHistoryEvent, 0, len(events))
	for _, event := range events {
		out = append(out, api.MemoryHistoryEvent{
			ID:         event.ID,
			Timestamp:  event.Timestamp,
			AgentKey:   event.AgentKey,
			ChatID:     event.ChatID,
			RunID:      event.RunID,
			RequestID:  event.RequestID,
			UserKey:    event.UserKey,
			MemoryID:   event.MemoryID,
			MemoryKind: event.MemoryKind,
			ScopeType:  event.ScopeType,
			ScopeKey:   event.ScopeKey,
			Operation:  event.Operation,
			Source:     event.Source,
			Status:     event.Status,
			Before:     event.Before,
			After:      event.After,
			Delta:      event.Delta,
			Meta:       event.Meta,
		})
	}
	return out
}
