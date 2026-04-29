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

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/schedule"
	"agent-platform-runner-go/internal/ws"
)

type scheduleStatusError struct {
	status  int
	code    string
	message string
}

func (e scheduleStatusError) Error() string { return e.message }

func newScheduleStatusError(status int, code string, message string) error {
	return scheduleStatusError{status: status, code: code, message: message}
}

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	var req api.ScheduleListRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.listSchedules(req)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.loadSchedule(req.ID)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.createSchedule(req)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.updateSchedule(req)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteSchedule(req)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) handleScheduleToggle(w http.ResponseWriter, r *http.Request) {
	var req api.ToggleScheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.toggleSchedule(req)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) handleScheduleExecutions(w http.ResponseWriter, r *http.Request) {
	var req api.ScheduleExecutionsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.listScheduleExecutions(req)
	s.writeScheduleHTTPResponse(w, response, err)
}

func (s *Server) writeScheduleHTTPResponse(w http.ResponseWriter, response any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	var statusErr scheduleStatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) scheduleDepsReady() error {
	if s == nil || s.deps.ScheduleRegistry == nil {
		return newScheduleStatusError(http.StatusServiceUnavailable, "unavailable", "schedule registry is not configured")
	}
	return nil
}

func (s *Server) listSchedules(_ api.ScheduleListRequest) (api.ScheduleListResponse, error) {
	if err := s.scheduleDepsReady(); err != nil {
		return api.ScheduleListResponse{}, err
	}
	defs, err := s.deps.ScheduleRegistry.Load()
	if err != nil {
		return api.ScheduleListResponse{}, err
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })

	active := map[string]schedule.ScheduleInfo{}
	if s.deps.ScheduleOrchestrator != nil {
		for _, item := range s.deps.ScheduleOrchestrator.Schedules() {
			active[item.Definition.ID] = item
		}
	}

	response := api.ScheduleListResponse{Items: make([]api.ScheduleSummaryResponse, 0, len(defs)), Total: len(defs)}
	for _, def := range defs {
		var next *time.Time
		if item, ok := active[def.ID]; ok && !item.NextFireTime.IsZero() {
			next = &item.NextFireTime
		}
		summary, err := s.mapScheduleSummary(def, next)
		if err != nil {
			return api.ScheduleListResponse{}, err
		}
		response.Items = append(response.Items, summary)
	}
	return response, nil
}

func (s *Server) loadSchedule(id string) (api.ScheduleDetailResponse, error) {
	def, err := s.findSchedule(id)
	if err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	var next *time.Time
	if s.deps.ScheduleOrchestrator != nil {
		for _, item := range s.deps.ScheduleOrchestrator.Schedules() {
			if item.Definition.ID == def.ID && !item.NextFireTime.IsZero() {
				next = &item.NextFireTime
				break
			}
		}
	}
	summary, err := s.mapScheduleSummary(def, next)
	if err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	return api.ScheduleDetailResponse{
		ScheduleSummaryResponse: summary,
		Query:                   mapScheduleQuery(def.Query),
	}, nil
}

func (s *Server) createSchedule(req api.CreateScheduleRequest) (api.ScheduleDetailResponse, error) {
	if err := s.scheduleDepsReady(); err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	id, err := s.nextScheduleID(req.Name)
	if err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	def := schedule.Definition{
		ID:            id,
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		Enabled:       enabled,
		Cron:          strings.TrimSpace(req.Cron),
		RemainingRuns: cloneIntPtr(req.RemainingRuns),
		AgentKey:      strings.TrimSpace(req.AgentKey),
		TeamID:        strings.TrimSpace(req.TeamID),
		Environment:   schedule.Environment{ZoneID: strings.TrimSpace(req.ZoneID)},
		Query:         scheduleQueryFromRequest(req.Query),
		SourceFile:    filepath.Join(s.deps.ScheduleRegistry.Root(), id+".yml"),
	}
	if err := s.deps.ScheduleRegistry.Persist(def); err != nil {
		return api.ScheduleDetailResponse{}, newScheduleStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if err := s.reloadSchedules(); err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	return s.loadSchedule(id)
}

func (s *Server) updateSchedule(req api.UpdateScheduleRequest) (api.ScheduleDetailResponse, error) {
	def, err := s.findSchedule(req.ID)
	if err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	applyScheduleUpdate(&def, req)
	if err := s.deps.ScheduleRegistry.Persist(def); err != nil {
		return api.ScheduleDetailResponse{}, newScheduleStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if err := s.reloadSchedules(); err != nil {
		return api.ScheduleDetailResponse{}, err
	}
	return s.loadSchedule(def.ID)
}

func (s *Server) deleteSchedule(req api.DeleteScheduleRequest) (map[string]any, error) {
	def, err := s.findSchedule(req.ID)
	if err != nil {
		return nil, err
	}
	if err := s.deps.ScheduleRegistry.Delete(def); err != nil {
		return nil, err
	}
	if err := s.reloadSchedules(); err != nil {
		return nil, err
	}
	return map[string]any{"id": def.ID, "deleted": true}, nil
}

func (s *Server) toggleSchedule(req api.ToggleScheduleRequest) (api.ScheduleDetailResponse, error) {
	return s.updateSchedule(api.UpdateScheduleRequest{ID: req.ID, Enabled: &req.Enabled})
}

func (s *Server) listScheduleExecutions(req api.ScheduleExecutionsRequest) (api.ScheduleExecutionListResponse, error) {
	if err := s.scheduleDepsReady(); err != nil {
		return api.ScheduleExecutionListResponse{}, err
	}
	if s.deps.ScheduleExecutions == nil {
		return api.ScheduleExecutionListResponse{}, newScheduleStatusError(http.StatusServiceUnavailable, "unavailable", "schedule execution store is not configured")
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return api.ScheduleExecutionListResponse{}, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	items, total, err := s.deps.ScheduleExecutions.ListBySchedule(id, req.Limit, req.Offset)
	if err != nil {
		return api.ScheduleExecutionListResponse{}, err
	}
	response := api.ScheduleExecutionListResponse{Items: make([]api.ScheduleExecutionResponse, 0, len(items)), Total: total}
	for _, item := range items {
		response.Items = append(response.Items, mapScheduleExecution(item))
	}
	return response, nil
}

func (s *Server) findSchedule(id string) (schedule.Definition, error) {
	if err := s.scheduleDepsReady(); err != nil {
		return schedule.Definition{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return schedule.Definition{}, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	defs, err := s.deps.ScheduleRegistry.Load()
	if err != nil {
		return schedule.Definition{}, err
	}
	for _, def := range defs {
		if def.ID == id {
			return def, nil
		}
	}
	return schedule.Definition{}, newScheduleStatusError(http.StatusNotFound, "not_found", "schedule not found")
}

func (s *Server) reloadSchedules() error {
	if s.deps.ScheduleOrchestrator == nil {
		return nil
	}
	if err := s.deps.ScheduleOrchestrator.Reload(); err != nil {
		return err
	}
	return nil
}

func (s *Server) mapScheduleSummary(def schedule.Definition, next *time.Time) (api.ScheduleSummaryResponse, error) {
	resp := api.ScheduleSummaryResponse{
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
	if s.deps.ScheduleExecutions != nil {
		last, err := s.deps.ScheduleExecutions.LastExecution(def.ID)
		if err != nil {
			return api.ScheduleSummaryResponse{}, err
		}
		if last != nil {
			resp.LastExecution = &api.ScheduleExecutionBrief{
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

func mapScheduleQuery(query schedule.Query) api.ScheduleQueryResponse {
	return api.ScheduleQueryResponse{
		Message: query.Message,
		ChatID:  query.ChatID,
		Role:    query.Role,
		Params:  cloneAnyMap(query.Params),
		Hidden:  cloneBoolPtr(query.Hidden),
	}
}

func scheduleQueryFromRequest(req api.ScheduleQueryRequest) schedule.Query {
	return schedule.Query{
		ChatID:  strings.TrimSpace(req.ChatID),
		Role:    strings.TrimSpace(req.Role),
		Message: strings.TrimSpace(req.Message),
		Params:  cloneAnyMap(req.Params),
		Hidden:  cloneBoolPtr(req.Hidden),
	}
}

func applyScheduleUpdate(def *schedule.Definition, req api.UpdateScheduleRequest) {
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
		def.Query.Params = cloneAnyMap(req.Query.Params)
		def.Query.Hidden = cloneBoolPtr(req.Query.Hidden)
	}
}

func mapScheduleExecution(item schedule.Execution) api.ScheduleExecutionResponse {
	return api.ScheduleExecutionResponse{
		ID:           item.ID,
		ScheduleID:   item.ScheduleID,
		ScheduleName: item.ScheduleName,
		SourceFile:   item.SourceFile,
		AgentKey:     item.AgentKey,
		TeamID:       item.TeamID,
		Status:       item.Status,
		Error:        item.Error,
		StartedAt:    item.StartedAt,
		CompletedAt:  cloneInt64Ptr(item.CompletedAt),
		DurationMs:   cloneInt64Ptr(item.DurationMs),
	}
}

func (s *Server) nextScheduleID(name string) (string, error) {
	base := scheduleSlug(name)
	existing := map[string]struct{}{}
	defs, err := s.deps.ScheduleRegistry.Load()
	if err != nil {
		return "", err
	}
	for _, def := range defs {
		existing[def.ID] = struct{}{}
	}
	root := strings.TrimSpace(s.deps.ScheduleRegistry.Root())
	for i := 0; i < 10; i++ {
		id := base
		if i > 0 {
			id = base + "-" + randomScheduleSuffix()
		}
		if _, ok := existing[id]; ok {
			continue
		}
		if root != "" {
			if scheduleFileExists(filepath.Join(root, id+".yml")) || scheduleFileExists(filepath.Join(root, id+".yaml")) {
				continue
			}
		}
		return id, nil
	}
	return "", newScheduleStatusError(http.StatusInternalServerError, "internal_error", "failed to allocate schedule id")
}

func scheduleSlug(name string) string {
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
		return "schedule"
	}
	return slug
}

func randomScheduleSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func scheduleFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
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

func cloneBoolPtr(src *bool) *bool {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func (s *Server) wsSchedules(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ScheduleListRequest](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, listErr := s.listSchedules(payload)
	s.sendScheduleWSResponse(conn, req, response, listErr)
}

func (s *Server) wsSchedule(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ID string `json:"id"`
	}](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, loadErr := s.loadSchedule(payload.ID)
	s.sendScheduleWSResponse(conn, req, response, loadErr)
}

func (s *Server) wsScheduleCreate(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.CreateScheduleRequest](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, createErr := s.createSchedule(payload)
	s.sendScheduleWSResponse(conn, req, response, createErr)
}

func (s *Server) wsScheduleUpdate(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.UpdateScheduleRequest](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, updateErr := s.updateSchedule(payload)
	s.sendScheduleWSResponse(conn, req, response, updateErr)
}

func (s *Server) wsScheduleDelete(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.DeleteScheduleRequest](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, deleteErr := s.deleteSchedule(payload)
	s.sendScheduleWSResponse(conn, req, response, deleteErr)
}

func (s *Server) wsScheduleToggle(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ToggleScheduleRequest](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, toggleErr := s.toggleSchedule(payload)
	s.sendScheduleWSResponse(conn, req, response, toggleErr)
}

func (s *Server) wsScheduleExecutions(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.ScheduleExecutionsRequest](req)
	if err != nil {
		s.sendScheduleWSError(conn, req, newScheduleStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, listErr := s.listScheduleExecutions(payload)
	s.sendScheduleWSResponse(conn, req, response, listErr)
}

func (s *Server) sendScheduleWSResponse(conn *ws.Conn, req ws.RequestFrame, response any, err error) {
	if err != nil {
		s.sendScheduleWSError(conn, req, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) sendScheduleWSError(conn *ws.Conn, req ws.RequestFrame, err error) {
	var statusErr scheduleStatusError
	if errors.As(err, &statusErr) {
		conn.SendError(req.ID, statusErr.code, statusErr.status, statusErr.message, nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
	conn.CompleteRequest(req.ID)
}
