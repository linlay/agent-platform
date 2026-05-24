package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/memory"
	"agent-platform/internal/ws"
)

func (s *Server) handleMemoryMeta(w http.ResponseWriter, r *http.Request) {
	response := api.MemoryMetaResponse{
		Categories:  memory.StandardCategories(),
		Types:       memory.StandardTypes(),
		ScopeTypes:  memory.StandardScopeTypes(),
		Statuses:    memory.StandardStatuses(),
		SourceTypes: memory.StandardSourceTypes(),
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleMemoryRecords(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	limit, ok := parseMemoryLimit(w, r, 20)
	if !ok {
		return
	}
	result, err := memory.ListConsoleRecords(s.deps.Memory, memory.RecordFilter{
		AgentKey:  strings.TrimSpace(r.URL.Query().Get("agentKey")),
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		ScopeType: strings.TrimSpace(r.URL.Query().Get("scopeType")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		Category:  strings.TrimSpace(r.URL.Query().Get("category")),
		ChatID:    strings.TrimSpace(r.URL.Query().Get("chatId")),
		Keyword:   firstQueryValue(r, "keyword", "query"),
		Limit:     limit,
		Cursor:    strings.TrimSpace(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.MemoryRecordsResponse{
		Count:      result.Count,
		NextCursor: result.NextCursor,
		Results:    result.Results,
	}))
}

func (s *Server) handleMemoryRecord(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	id := firstNonBlank(r.URL.Query().Get("recordId"), r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "id is required"))
		return
	}
	detail, err := memory.ReadConsoleRecord(s.deps.Memory, strings.TrimSpace(r.URL.Query().Get("agentKey")), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if strings.TrimSpace(detail.Record.ID) == "" {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "memory not found"))
		return
	}
	embedding := api.MemoryRecordEmbedding{HasEmbedding: detail.HasEmbedding}
	if detail.EmbeddingModel != nil {
		embedding.Model = *detail.EmbeddingModel
	}
	writeJSON(w, http.StatusOK, api.Success(api.MemoryRecordDetailResponse{
		ID:          detail.Record.ID,
		SourceTable: detail.SourceTable,
		Record:      detail.Record,
		RawFields:   detail.RawFields,
		Embedding:   embedding,
	}))
}

func (s *Server) handleMemoryRecordTimeline(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.deps.Memory.(memory.HistoryProvider)
	if !s.memorySystemEnabled() || s.deps.Memory == nil || !ok {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory history is not configured"))
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "id is required"))
		return
	}
	limit, ok := parseMemoryLimit(w, r, 50)
	if !ok {
		return
	}
	result, err := provider.History(memory.HistoryFilter{
		AgentKey: strings.TrimSpace(r.URL.Query().Get("agentKey")),
		MemoryID: id,
		Limit:    limit,
		Cursor:   strings.TrimSpace(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.MemoryRecordTimelineResponse{
		ID:     id,
		Events: toMemoryHistoryEvents(result.Events),
	}))
}

func (s *Server) wsMemoryMeta(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	sendMemoryWSResponse(conn, req, api.MemoryMetaResponse{
		Categories:  memory.StandardCategories(),
		Types:       memory.StandardTypes(),
		ScopeTypes:  memory.StandardScopeTypes(),
		Statuses:    memory.StandardStatuses(),
		SourceTypes: memory.StandardSourceTypes(),
	})
}

func (s *Server) wsMemoryRecords(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	payload, err := ws.DecodePayload[struct {
		AgentKey  string `json:"agentKey"`
		Kind      string `json:"kind"`
		ScopeType string `json:"scopeType"`
		Status    string `json:"status"`
		Category  string `json:"category"`
		ChatID    string `json:"chatId"`
		Keyword   string `json:"keyword"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
		Cursor    string `json:"cursor"`
	}](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	limit := payload.Limit
	if limit == 0 {
		limit = 20
	}
	keyword := strings.TrimSpace(payload.Keyword)
	if keyword == "" {
		keyword = strings.TrimSpace(payload.Query)
	}
	result, err := memory.ListConsoleRecords(s.deps.Memory, memory.RecordFilter{
		AgentKey:  strings.TrimSpace(payload.AgentKey),
		Kind:      strings.TrimSpace(payload.Kind),
		ScopeType: strings.TrimSpace(payload.ScopeType),
		Status:    strings.TrimSpace(payload.Status),
		Category:  strings.TrimSpace(payload.Category),
		ChatID:    strings.TrimSpace(payload.ChatID),
		Keyword:   keyword,
		Limit:     limit,
		Cursor:    strings.TrimSpace(payload.Cursor),
	})
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	sendMemoryWSResponse(conn, req, api.MemoryRecordsResponse{
		Count:      result.Count,
		NextCursor: result.NextCursor,
		Results:    result.Results,
	})
}

func (s *Server) wsMemoryRecord(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
		ID       string `json:"id"`
		RecordID string `json:"recordId"`
	}](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	id := firstNonBlank(payload.RecordID, payload.ID)
	if id == "" {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "id is required")
		return
	}
	detail, err := memory.ReadConsoleRecord(s.deps.Memory, strings.TrimSpace(payload.AgentKey), id)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if strings.TrimSpace(detail.Record.ID) == "" {
		sendMemoryWSError(conn, req, http.StatusNotFound, "not_found", "memory not found")
		return
	}
	embedding := api.MemoryRecordEmbedding{HasEmbedding: detail.HasEmbedding}
	if detail.EmbeddingModel != nil {
		embedding.Model = *detail.EmbeddingModel
	}
	sendMemoryWSResponse(conn, req, api.MemoryRecordDetailResponse{
		ID:          detail.Record.ID,
		SourceTable: detail.SourceTable,
		Record:      detail.Record,
		RawFields:   detail.RawFields,
		Embedding:   embedding,
	})
}

func sendMemoryWSResponse(conn *ws.Conn, req ws.RequestFrame, response any) {
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func sendMemoryWSError(conn *ws.Conn, req ws.RequestFrame, status int, code string, message string) {
	conn.SendError(req.ID, code, status, message, nil)
	conn.CompleteRequest(req.ID)
}

func parseMemoryLimit(w http.ResponseWriter, r *http.Request, fallback int) (int, bool) {
	limit := fallback
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "limit must be an integer"))
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}
