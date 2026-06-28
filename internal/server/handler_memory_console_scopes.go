package server

import (
	"context"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/memory"
	"agent-platform/internal/ws"
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

func (s *Server) wsMemoryScopes(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
		TeamID   string `json:"teamId"`
		UserKey  string `json:"userKey"`
	}](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	agentKey := strings.TrimSpace(payload.AgentKey)
	if agentKey == "" {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "agentKey is required")
		return
	}
	views, err := memory.BuildScopeSummaries(s.deps.Memory, agentKey, scopeUserKeyFromContext(ctx, payload.UserKey), strings.TrimSpace(payload.TeamID))
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusInternalServerError, "internal_error", err.Error())
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
	sendMemoryWSResponse(conn, req, response)
}

func (s *Server) wsMemoryScopeDetail(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		AgentKey  string `json:"agentKey"`
		ScopeType string `json:"scopeType"`
		ScopeKey  string `json:"scopeKey"`
		TeamID    string `json:"teamId"`
		UserKey   string `json:"userKey"`
	}](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	s.wsMemoryScopeGet(ctx, conn, req, payload.AgentKey, payload.ScopeType, payload.ScopeKey, payload.TeamID, payload.UserKey)
}

func (s *Server) wsMemoryScopeSaveRoute(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		api.MemoryScopeSaveRequest
		TeamID  string `json:"teamId"`
		UserKey string `json:"userKey"`
	}](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	s.wsMemoryScopeSave(ctx, conn, req, payload.MemoryScopeSaveRequest, payload.TeamID, payload.UserKey)
}

func (s *Server) wsMemoryScopeGet(ctx context.Context, conn *ws.Conn, req ws.RequestFrame, agentKey string, scopeType string, scopeKey string, teamID string, userKey string) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	agentKey = strings.TrimSpace(agentKey)
	scopeType = strings.TrimSpace(scopeType)
	if agentKey == "" || scopeType == "" {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "agentKey and scopeType are required")
		return
	}
	view, err := memory.BuildScopeView(
		s.deps.Memory,
		agentKey,
		scopeType,
		strings.TrimSpace(scopeKey),
		scopeUserKeyFromContext(ctx, userKey),
		strings.TrimSpace(teamID),
	)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", err.Error())
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
	sendMemoryWSResponse(conn, req, response)
}

func (s *Server) wsMemoryScopeSave(ctx context.Context, conn *ws.Conn, req ws.RequestFrame, payload api.MemoryScopeSaveRequest, teamID string, userKey string) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	input := memory.ScopeSaveInput{
		AgentKey:       strings.TrimSpace(payload.AgentKey),
		ScopeType:      strings.TrimSpace(payload.ScopeType),
		ScopeKey:       strings.TrimSpace(payload.ScopeKey),
		UserKey:        scopeUserKeyFromContext(ctx, userKey),
		TeamID:         strings.TrimSpace(teamID),
		Mode:           strings.TrimSpace(payload.Mode),
		Markdown:       payload.Markdown,
		ArchiveMissing: payload.ArchiveMissing,
		Records:        make([]memory.ScopeRecordInput, 0, len(payload.Records)),
	}
	for _, record := range payload.Records {
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
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	view, err := memory.BuildScopeView(s.deps.Memory, input.AgentKey, input.ScopeType, input.ScopeKey, input.UserKey, input.TeamID)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusInternalServerError, "internal_error", err.Error())
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
	sendMemoryWSResponse(conn, req, response)
}

func (s *Server) wsMemoryScopeValidate(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if !s.memorySystemEnabled() || s.deps.Memory == nil {
		sendMemoryWSError(conn, req, http.StatusServiceUnavailable, "unavailable", "memory system is disabled")
		return
	}
	payload, err := ws.DecodePayload[api.MemoryScopeValidateRequest](req)
	if err != nil {
		sendMemoryWSError(conn, req, http.StatusBadRequest, "invalid_request", "invalid payload")
		return
	}
	result := memory.ValidateScopeMarkdown(payload.ScopeType, payload.Markdown)
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
	sendMemoryWSResponse(conn, req, response)
}

func scopeUserKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	return scopeUserKeyFromContext(r.Context(), r.URL.Query().Get("userKey"))
}

func scopeUserKeyFromContext(ctx context.Context, explicit string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	principal := PrincipalFromContext(ctx)
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
