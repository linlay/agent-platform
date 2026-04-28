package server

import (
	"net/http"
	"strconv"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/memory"
)

func (s *Server) handleMemoryScopes(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey is required"))
		return
	}
	views, err := memory.BuildScopeSummaries(s.deps.Memory, agentKey, scopeUserKey(r), strings.TrimSpace(r.URL.Query().Get("teamId")))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	response := api.MemoryScopesResponse{AgentKey: agentKey, Scopes: make([]api.MemoryScopeSummary, 0, len(views))}
	for _, view := range views {
		response.Scopes = append(response.Scopes, api.MemoryScopeSummary{
			ScopeType:   view.ScopeType,
			ScopeKey:    view.ScopeKey,
			Label:       view.Label,
			FileName:    view.FileName,
			RecordCount: len(view.Records),
			UpdatedAt:   view.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleMemoryScopeRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleMemoryScope(w, r)
	case http.MethodPut:
		s.handleMemoryScopeSave(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) handleMemoryScope(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	scopeType := strings.TrimSpace(r.URL.Query().Get("scopeType"))
	if agentKey == "" || scopeType == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey and scopeType are required"))
		return
	}
	view, err := memory.BuildScopeView(
		s.deps.Memory,
		agentKey,
		scopeType,
		strings.TrimSpace(r.URL.Query().Get("scopeKey")),
		scopeUserKey(r),
		strings.TrimSpace(r.URL.Query().Get("teamId")),
	)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	response := api.MemoryScopeDetailResponse{
		AgentKey:  view.AgentKey,
		ScopeType: view.ScopeType,
		ScopeKey:  view.ScopeKey,
		Label:     view.Label,
		FileName:  view.FileName,
		Markdown:  view.Markdown,
		Records:   make([]api.MemoryScopeRecord, 0, len(view.Records)),
		Meta: api.MemoryScopeDetailMeta{
			Editable:           true,
			RecordCount:        len(view.Records),
			GeneratedFromStore: true,
		},
	}
	for _, item := range view.Records {
		response.Records = append(response.Records, toMemoryScopeRecord(item))
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleMemoryScopeSave(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	var req api.MemoryScopeSaveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	input := memory.ScopeSaveInput{
		AgentKey:       strings.TrimSpace(req.AgentKey),
		ScopeType:      strings.TrimSpace(req.ScopeType),
		ScopeKey:       strings.TrimSpace(req.ScopeKey),
		UserKey:        scopeUserKey(r),
		TeamID:         strings.TrimSpace(r.URL.Query().Get("teamId")),
		Mode:           strings.TrimSpace(req.Mode),
		Markdown:       req.Markdown,
		ArchiveMissing: req.ArchiveMissing,
		Records:        make([]memory.ScopeRecordInput, 0, len(req.Records)),
	}
	for _, record := range req.Records {
		input.Records = append(input.Records, memory.ScopeRecordInput{
			ID:         record.ID,
			Title:      record.Title,
			Summary:    record.Summary,
			Category:   record.Category,
			Importance: record.Importance,
			Confidence: record.Confidence,
			Tags:       record.Tags,
		})
	}
	result, err := memory.SaveScope(s.deps.Memory, input)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	view, err := memory.BuildScopeView(s.deps.Memory, input.AgentKey, input.ScopeType, input.ScopeKey, input.UserKey, input.TeamID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	response := api.MemoryScopeSaveResponse{
		Saved:     true,
		AgentKey:  input.AgentKey,
		ScopeType: view.ScopeType,
		ScopeKey:  view.ScopeKey,
		Summary: api.MemoryScopeSaveSummary{
			Created:   result.Summary.Created,
			Updated:   result.Summary.Updated,
			Archived:  result.Summary.Archived,
			Unchanged: result.Summary.Unchanged,
		},
		Records:  make([]api.MemoryScopeSaveRecord, 0, len(result.Records)),
		Markdown: result.Markdown,
	}
	for _, item := range result.Records {
		response.Records = append(response.Records, api.MemoryScopeSaveRecord{
			ID:        item.ID,
			Title:     item.Title,
			Status:    item.Status,
			ScopeType: item.ScopeType,
			ScopeKey:  item.ScopeKey,
			UpdatedAt: item.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleMemoryScopeValidate(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	var req api.MemoryScopeValidateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	result := memory.ValidateScopeMarkdown(req.ScopeType, req.Markdown)
	response := api.MemoryScopeValidateResponse{
		Valid:    result.Valid,
		Errors:   make([]api.MemoryScopeValidationIssue, 0, len(result.Errors)),
		Warnings: make([]api.MemoryScopeValidationIssue, 0, len(result.Warnings)),
	}
	for _, issue := range result.Errors {
		response.Errors = append(response.Errors, api.MemoryScopeValidationIssue(issue))
	}
	for _, issue := range result.Warnings {
		response.Warnings = append(response.Warnings, api.MemoryScopeValidationIssue(issue))
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleMemoryRecords(w http.ResponseWriter, r *http.Request) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "memory system is disabled"))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "limit must be an integer"))
			return
		}
		limit = parsed
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
	id := strings.TrimSpace(r.URL.Query().Get("id"))
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

func scopeUserKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if value := strings.TrimSpace(r.URL.Query().Get("userKey")); value != "" {
		return value
	}
	principal := PrincipalFromContext(r.Context())
	if principal != nil {
		return strings.TrimSpace(principal.Subject)
	}
	return ""
}

func toMemoryScopeRecord(item api.StoredMemoryResponse) api.MemoryScopeRecord {
	return api.MemoryScopeRecord{
		ID:         item.ID,
		Title:      item.Title,
		Summary:    item.Summary,
		Category:   item.Category,
		Importance: item.Importance,
		Confidence: item.Confidence,
		Status:     item.Status,
		ScopeType:  item.ScopeType,
		ScopeKey:   item.ScopeKey,
		Tags:       append([]string(nil), item.Tags...),
		CreatedAt:  item.CreatedAt,
		UpdatedAt:  item.UpdatedAt,
	}
}

func firstQueryValue(r *http.Request, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			return value
		}
	}
	return ""
}
