package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"
)

type automationStatusError struct {
	status  int
	code    string
	message string
}

func (e automationStatusError) Error() string { return e.message }

func newAutomationStatusError(status int, code string, message string) error {
	return automationStatusError{status: status, code: code, message: message}
}

func (s *Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	var req api.AutomationListRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.listAutomations(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) handleAutomation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.loadAutomation(req.ID)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) handleAutomationCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAutomationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.createAutomation(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) handleAutomationUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateAutomationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	id, err := queryOrBodyIDAny(r, []string{"automationId", "id"}, req.AutomationID, req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.ID = id
	response, err := s.updateAutomation(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) handleAutomationDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAutomationRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	id, err := queryOrBodyIDAny(r, []string{"automationId", "id"}, req.AutomationID, req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.ID = id
	response, err := s.deleteAutomation(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) handleAutomationToggle(w http.ResponseWriter, r *http.Request) {
	var req api.ToggleAutomationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	id, err := queryOrBodyIDAny(r, []string{"automationId", "id"}, req.AutomationID, req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.ID = id
	response, err := s.toggleAutomation(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) handleAutomationExecutions(w http.ResponseWriter, r *http.Request) {
	var req api.AutomationExecutionsRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	id, err := queryOrBodyIDAny(r, []string{"automationId", "id"}, req.AutomationID, req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.ID = id
	response, err := s.listAutomationExecutions(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) writeAutomationHTTPResponse(w http.ResponseWriter, response any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	var statusErr automationStatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) automationDepsReady() error {
	if s == nil || s.deps.AutomationRegistry == nil {
		return newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation registry is not configured")
	}
	return nil
}

func (s *Server) listAutomations(_ api.AutomationListRequest) (api.AutomationListResponse, error) {
	if err := s.automationDepsReady(); err != nil {
		return api.AutomationListResponse{}, err
	}
	defs, err := s.deps.AutomationRegistry.Load()
	if err != nil {
		return api.AutomationListResponse{}, err
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })

	active := map[string]automation.AutomationInfo{}
	if s.deps.AutomationOrchestrator != nil {
		for _, item := range s.deps.AutomationOrchestrator.Automations() {
			active[item.Definition.ID] = item
		}
	}

	response := api.AutomationListResponse{Items: make([]api.AutomationSummaryResponse, 0, len(defs)), Total: len(defs)}
	for _, def := range defs {
		var next *time.Time
		if item, ok := active[def.ID]; ok && !item.NextFireTime.IsZero() {
			next = &item.NextFireTime
		}
		summary, err := s.mapAutomationSummary(def, next)
		if err != nil {
			return api.AutomationListResponse{}, err
		}
		response.Items = append(response.Items, summary)
	}
	return response, nil
}

func (s *Server) loadAutomation(id string) (api.AutomationDetailResponse, error) {
	def, err := s.findAutomation(id)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	var next *time.Time
	if s.deps.AutomationOrchestrator != nil {
		for _, item := range s.deps.AutomationOrchestrator.Automations() {
			if item.Definition.ID == def.ID && !item.NextFireTime.IsZero() {
				next = &item.NextFireTime
				break
			}
		}
	}
	summary, err := s.mapAutomationSummary(def, next)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	return api.AutomationDetailResponse{
		AutomationSummaryResponse: summary,
		Query:                     mapAutomationQuery(def.Query),
	}, nil
}

func (s *Server) createAutomation(req api.CreateAutomationRequest) (api.AutomationDetailResponse, error) {
	if err := s.automationDepsReady(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	id, err := s.nextAutomationID(req.Name)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	def := automation.Definition{
		ID:            id,
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		Enabled:       enabled,
		Cron:          strings.TrimSpace(req.Cron),
		RemainingRuns: cloneIntPtr(req.RemainingRuns),
		AgentKey:      strings.TrimSpace(req.AgentKey),
		TeamID:        strings.TrimSpace(req.TeamID),
		Environment:   automation.Environment{ZoneID: strings.TrimSpace(req.ZoneID)},
		Query:         automationQueryFromRequest(req.Query),
		SourceFile:    filepath.Join(s.deps.AutomationRegistry.Root(), id+".yml"),
	}
	if err := s.deps.AutomationRegistry.Persist(def); err != nil {
		return api.AutomationDetailResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if err := s.reloadAutomations(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	return s.loadAutomation(id)
}

func (s *Server) updateAutomation(req api.UpdateAutomationRequest) (api.AutomationDetailResponse, error) {
	req.ID = firstNonBlank(req.ID, req.AutomationID)
	def, err := s.findAutomation(req.ID)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	applyAutomationUpdate(&def, req)
	if err := s.deps.AutomationRegistry.Persist(def); err != nil {
		return api.AutomationDetailResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if err := s.reloadAutomations(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	return s.loadAutomation(def.ID)
}

func (s *Server) deleteAutomation(req api.DeleteAutomationRequest) (map[string]any, error) {
	req.ID = firstNonBlank(req.ID, req.AutomationID)
	def, err := s.findAutomation(req.ID)
	if err != nil {
		return nil, err
	}
	if err := s.deps.AutomationRegistry.Delete(def); err != nil {
		return nil, err
	}
	if err := s.reloadAutomations(); err != nil {
		return nil, err
	}
	return map[string]any{"id": def.ID, "deleted": true}, nil
}

func (s *Server) toggleAutomation(req api.ToggleAutomationRequest) (api.AutomationDetailResponse, error) {
	req.ID = firstNonBlank(req.ID, req.AutomationID)
	return s.updateAutomation(api.UpdateAutomationRequest{ID: req.ID, Enabled: &req.Enabled})
}

func (s *Server) listAutomationExecutions(req api.AutomationExecutionsRequest) (api.AutomationExecutionListResponse, error) {
	if err := s.automationDepsReady(); err != nil {
		return api.AutomationExecutionListResponse{}, err
	}
	if s.deps.AutomationExecutions == nil {
		return api.AutomationExecutionListResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation execution store is not configured")
	}
	id := firstNonBlank(req.ID, req.AutomationID)
	if id == "" {
		return api.AutomationExecutionListResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	items, total, err := s.deps.AutomationExecutions.ListByAutomation(id, req.Limit, req.Offset)
	if err != nil {
		return api.AutomationExecutionListResponse{}, err
	}
	response := api.AutomationExecutionListResponse{Items: make([]api.AutomationExecutionResponse, 0, len(items)), Total: total}
	for _, item := range items {
		response.Items = append(response.Items, mapAutomationExecution(item))
	}
	return response, nil
}

func (s *Server) findAutomation(id string) (automation.Definition, error) {
	if err := s.automationDepsReady(); err != nil {
		return automation.Definition{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return automation.Definition{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	defs, err := s.deps.AutomationRegistry.Load()
	if err != nil {
		return automation.Definition{}, err
	}
	for _, def := range defs {
		if def.ID == id {
			return def, nil
		}
	}
	return automation.Definition{}, newAutomationStatusError(http.StatusNotFound, "not_found", "automation not found")
}

func (s *Server) reloadAutomations() error {
	if s.deps.AutomationOrchestrator == nil {
		return nil
	}
	if err := s.deps.AutomationOrchestrator.Reload(); err != nil {
		return err
	}
	return nil
}

func (s *Server) mapAutomationSummary(def automation.Definition, next *time.Time) (api.AutomationSummaryResponse, error) {
	resp := api.AutomationSummaryResponse{
		ID:            def.ID,
		Name:          def.Name,
		Description:   def.Description,
		Cron:          def.Cron,
		AgentKey:      def.AgentKey,
		Enabled:       def.Enabled,
		TeamID:        def.TeamID,
		ZoneID:        def.Environment.ZoneID,
		SourceFile:    def.SourceFile,
		RemainingRuns: cloneIntPtr(def.RemainingRuns),
	}
	if next != nil && !next.IsZero() {
		formatted := next.Format(time.RFC3339)
		resp.NextFireTime = &formatted
	}
	if s.deps.AutomationExecutions != nil {
		last, err := s.deps.AutomationExecutions.LastExecution(def.ID)
		if err != nil {
			return api.AutomationSummaryResponse{}, err
		}
		if last != nil {
			resp.LastExecution = &api.AutomationExecutionBrief{
				ID:         last.ID,
				Status:     last.Status,
				StartedAt:  last.StartedAt,
				DurationMs: cloneInt64Ptr(last.DurationMs),
				Error:      last.Error,
			}
		}
	}
	return resp, nil
}

func mapAutomationQuery(query automation.Query) api.AutomationQueryResponse {
	return api.AutomationQueryResponse{
		Message: query.Message,
		ChatID:  query.ChatID,
		Role:    automation.EffectiveQueryRole(query.Role),
		Params:  contracts.CloneAnyMap(query.Params),
	}
}

func automationQueryFromRequest(req api.AutomationQueryRequest) automation.Query {
	return automation.Query{
		ChatID:  strings.TrimSpace(req.ChatID),
		Role:    strings.TrimSpace(req.Role),
		Message: strings.TrimSpace(req.Message),
		Params:  contracts.CloneAnyMap(req.Params),
	}
}

func applyAutomationUpdate(def *automation.Definition, req api.UpdateAutomationRequest) {
	if req.Name != nil {
		def.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		def.Description = strings.TrimSpace(*req.Description)
	}
	if req.Cron != nil {
		def.Cron = strings.TrimSpace(*req.Cron)
	}
	if req.AgentKey != nil {
		def.AgentKey = strings.TrimSpace(*req.AgentKey)
	}
	if req.TeamID != nil {
		def.TeamID = strings.TrimSpace(*req.TeamID)
	}
	if req.ZoneID != nil {
		def.Environment.ZoneID = strings.TrimSpace(*req.ZoneID)
	}
	if req.Enabled != nil {
		def.Enabled = *req.Enabled
	}
	if req.RemainingRuns != nil {
		def.RemainingRuns = cloneIntPtr(req.RemainingRuns)
	}
	if req.Query != nil {
		def.Query.ChatID = strings.TrimSpace(req.Query.ChatID)
		def.Query.Role = strings.TrimSpace(req.Query.Role)
		def.Query.Message = strings.TrimSpace(req.Query.Message)
		def.Query.Params = contracts.CloneAnyMap(req.Query.Params)
	}
}

func mapAutomationExecution(item automation.Execution) api.AutomationExecutionResponse {
	return api.AutomationExecutionResponse{
		ID:             item.ID,
		AutomationID:   item.AutomationID,
		AutomationName: item.AutomationName,
		SourceFile:     item.SourceFile,
		AgentKey:       item.AgentKey,
		TeamID:         item.TeamID,
		Status:         item.Status,
		Error:          item.Error,
		StartedAt:      item.StartedAt,
		CompletedAt:    cloneInt64Ptr(item.CompletedAt),
		DurationMs:     cloneInt64Ptr(item.DurationMs),
	}
}

func (s *Server) nextAutomationID(name string) (string, error) {
	base := automationSlug(name)
	existing := map[string]struct{}{}
	defs, err := s.deps.AutomationRegistry.Load()
	if err != nil {
		return "", err
	}
	for _, def := range defs {
		existing[def.ID] = struct{}{}
	}
	root := strings.TrimSpace(s.deps.AutomationRegistry.Root())
	for i := 0; i < 10; i++ {
		id := base
		if i > 0 {
			id = base + "-" + randomAutomationSuffix()
		}
		if _, ok := existing[id]; ok {
			continue
		}
		if root != "" {
			if automationFileExists(filepath.Join(root, id+".yml")) || automationFileExists(filepath.Join(root, id+".yaml")) {
				continue
			}
		}
		return id, nil
	}
	return "", newAutomationStatusError(http.StatusInternalServerError, "internal_error", "failed to allocate automation id")
}

func automationSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "automation"
	}
	return slug
}

func randomAutomationSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func automationFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cloneIntPtr(src *int) *int {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func (s *Server) wsAutomations(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.AutomationListRequest](req)
	if err != nil {
		s.sendAutomationWSError(conn, req, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, listErr := s.listAutomations(payload)
	s.sendAutomationWSResponse(conn, req, response, listErr)
}

func (s *Server) wsAutomation(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ID string `json:"id"`
	}](req)
	if err != nil {
		s.sendAutomationWSError(conn, req, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, loadErr := s.loadAutomation(payload.ID)
	s.sendAutomationWSResponse(conn, req, response, loadErr)
}

func (s *Server) wsAutomationExecutions(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.AutomationExecutionsRequest](req)
	if err != nil {
		s.sendAutomationWSError(conn, req, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	payload.ID = firstNonBlank(payload.AutomationID, payload.ID)
	response, listErr := s.listAutomationExecutions(payload)
	s.sendAutomationWSResponse(conn, req, response, listErr)
}

func (s *Server) sendAutomationWSResponse(conn *ws.Conn, req ws.RequestFrame, response any, err error) {
	if err != nil {
		s.sendAutomationWSError(conn, req, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) sendAutomationWSError(conn *ws.Conn, req ws.RequestFrame, err error) {
	var statusErr automationStatusError
	if errors.As(err, &statusErr) {
		conn.SendError(req.ID, statusErr.code, statusErr.status, statusErr.message, nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
	conn.CompleteRequest(req.ID)
}
