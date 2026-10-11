package automation

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Service is shared by HTTP and the trusted Platform Control tools.
type Service struct {
	Registry      *Registry
	Orchestrator  *Orchestrator
	History       ExecutionHistoryReader
	DefaultZoneID string
	ReceiptDir    string
}
type StatusError struct {
	Status        int
	Code, Message string
}

func (e StatusError) Error() string { return e.Message }
func newAutomationStatusError(status int, code, message string) error {
	return StatusError{status, code, message}
}
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func (s *Service) AutomationDepsReady() error {
	if s == nil || s.Registry == nil {
		return newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation registry is not configured")
	}
	return nil
}

func (s *Service) ListAutomations(_ api.AutomationListRequest) (api.AutomationListResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.AutomationListResponse{}, err
	}
	defs, err := s.Registry.Load()
	if err != nil {
		return api.AutomationListResponse{}, err
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })

	active := map[string]AutomationInfo{}
	if s.Orchestrator != nil {
		for _, item := range s.Orchestrator.Automations() {
			active[item.Definition.ID] = item
		}
	}

	response := api.AutomationListResponse{
		Items:            make([]api.AutomationSummaryResponse, 0, len(defs)),
		Total:            len(defs),
		ExecutionHistory: s.AutomationExecutionHistoryStatus(),
	}
	for _, def := range defs {
		var next *time.Time
		if item, ok := active[def.ID]; ok && !item.NextFireTime.IsZero() {
			next = &item.NextFireTime
		}
		summary, err := s.MapAutomationSummary(def, next)
		if err != nil {
			return api.AutomationListResponse{}, err
		}
		response.Items = append(response.Items, summary)
	}
	return response, nil
}

func (s *Service) LoadAutomation(id string) (api.AutomationDetailResponse, error) {
	def, err := s.FindAutomation(id)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	var next *time.Time
	if s.Orchestrator != nil {
		for _, item := range s.Orchestrator.Automations() {
			if item.Definition.ID == def.ID && !item.NextFireTime.IsZero() {
				next = &item.NextFireTime
				break
			}
		}
	}
	summary, err := s.MapAutomationSummary(def, next)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	return api.AutomationDetailResponse{
		AutomationSummaryResponse: summary,
		Query:                     mapAutomationQuery(def.Query),
		ExecutionHistory:          s.AutomationExecutionHistoryStatus(),
	}, nil
}

func (s *Service) createAutomation(req api.CreateAutomationRequest) (api.AutomationDetailResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	id, err := s.NextAutomationID(req.Name)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	def := Definition{
		ID:            id,
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		Enabled:       enabled,
		Cron:          strings.TrimSpace(req.Cron),
		RemainingRuns: cloneIntPtr(req.RemainingRuns),
		AgentKey:      strings.TrimSpace(req.AgentKey),

		Environment: Environment{ZoneID: strings.TrimSpace(req.ZoneID)},
		Query:       automationQueryFromRequest(req.Query),
		SourceFile:  filepath.Join(s.Registry.Root(), id+".yml"),
	}
	if err := s.Registry.applyControlDefinition(def, "", false); err != nil {
		return api.AutomationDetailResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if err := s.ReloadAutomations(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	return s.LoadAutomation(id)
}

func (s *Service) updateAutomation(req api.UpdateAutomationRequest) (api.AutomationDetailResponse, error) {
	req.ID = firstNonBlank(req.ID, req.AutomationID)
	def, err := s.FindAutomation(req.ID)
	if err != nil {
		return api.AutomationDetailResponse{}, err
	}
	revision := definitionRevision(def)
	applyAutomationUpdate(&def, req)
	if err := s.Registry.applyControlDefinition(def, revision, false); err != nil {
		return api.AutomationDetailResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if err := s.ReloadAutomations(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	return s.LoadAutomation(def.ID)
}

func (s *Service) deleteAutomation(req api.DeleteAutomationRequest) (map[string]any, error) {
	req.ID = firstNonBlank(req.ID, req.AutomationID)
	def, err := s.FindAutomation(req.ID)
	if err != nil {
		return nil, err
	}
	if err := s.Registry.applyControlDefinition(def, definitionRevision(def), true); err != nil {
		return nil, err
	}
	if err := s.ReloadAutomations(); err != nil {
		return nil, err
	}
	return map[string]any{"id": def.ID, "deleted": true}, nil
}

func (s *Service) ToggleAutomation(req api.ToggleAutomationRequest) (api.AutomationDetailResponse, error) {
	req.ID = firstNonBlank(req.ID, req.AutomationID)
	return s.UpdateAutomation(api.UpdateAutomationRequest{ID: req.ID, Enabled: &req.Enabled})
}

func (s *Service) triggerAutomation(id string) (api.TriggerAutomationResponse, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return api.TriggerAutomationResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	if err := s.AutomationDepsReady(); err != nil {
		return api.TriggerAutomationResponse{}, err
	}
	if s.Orchestrator == nil {
		return api.TriggerAutomationResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation orchestrator is not configured")
	}
	execution, err := s.Orchestrator.Trigger(id)
	if err != nil {
		switch {
		case errors.Is(err, ErrAutomationNotFound):
			return api.TriggerAutomationResponse{}, newAutomationStatusError(http.StatusNotFound, "not_found", "automation not found")
		case errors.Is(err, ErrOrchestratorUnavailable):
			return api.TriggerAutomationResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation orchestrator is unavailable")
		default:
			return api.TriggerAutomationResponse{}, err
		}
	}
	return api.TriggerAutomationResponse{
		Accepted:     true,
		Status:       "accepted",
		AutomationID: execution.AutomationID,
		ExecutionID:  execution.ID,
	}, nil
}

func (s *Service) ListAutomationExecutions(req api.AutomationExecutionsRequest) (api.AutomationExecutionListResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.AutomationExecutionListResponse{}, err
	}
	if s.History == nil {
		return api.AutomationExecutionListResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation execution history is not configured")
	}
	status := s.History.Status()
	if !status.Available {
		return api.AutomationExecutionListResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", firstNonBlank(status.Message, "automation execution history is unavailable"))
	}
	id := firstNonBlank(req.ID, req.AutomationID)
	if id == "" {
		return api.AutomationExecutionListResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	items, total, err := s.History.ListByAutomation(id, req.Limit, req.Offset)
	if err != nil {
		return api.AutomationExecutionListResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation execution history is unreadable: "+err.Error())
	}
	loc := s.AutomationDisplayLocation()
	response := api.AutomationExecutionListResponse{Items: make([]api.AutomationExecutionResponse, 0, len(items)), Total: total}
	for _, item := range items {
		response.Items = append(response.Items, mapAutomationExecution(item, loc))
	}
	return response, nil
}

func (s *Service) LoadAutomationExecution(req api.AutomationExecutionRequest) (api.AutomationExecutionDetailResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.AutomationExecutionDetailResponse{}, err
	}
	if s.History == nil {
		return api.AutomationExecutionDetailResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation execution history is not configured")
	}
	status := s.History.Status()
	if !status.Available {
		return api.AutomationExecutionDetailResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", firstNonBlank(status.Message, "automation execution history is unavailable"))
	}
	executionID := firstNonBlank(req.ExecutionID, req.ID)
	if executionID == "" {
		return api.AutomationExecutionDetailResponse{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "executionId is required")
	}
	item, err := s.History.GetExecution(executionID)
	if err != nil {
		return api.AutomationExecutionDetailResponse{}, newAutomationStatusError(http.StatusServiceUnavailable, "unavailable", "automation execution history is unreadable: "+err.Error())
	}
	if item == nil {
		return api.AutomationExecutionDetailResponse{}, newAutomationStatusError(http.StatusNotFound, "not_found", "automation execution not found")
	}
	return api.AutomationExecutionDetailResponse{
		AutomationExecutionResponse: mapAutomationExecution(*item, s.AutomationDisplayLocation()),
		QueryContent:                item.QueryContent,
		ResultContent:               item.ResultContent,
	}, nil
}

func (s *Service) AutomationExecutionHistoryStatus() api.AutomationExecutionHistoryStatus {
	if s == nil || s.History == nil {
		return api.AutomationExecutionHistoryStatus{State: string(ExecutionHistoryUnavailable), Message: "automation execution history is not configured"}
	}
	status := s.History.Status()
	return api.AutomationExecutionHistoryStatus{
		Available: status.Available,
		State:     string(status.State),
		Message:   status.Message,
	}
}

func (s *Service) FindAutomation(id string) (Definition, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return Definition{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Definition{}, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "id is required")
	}
	defs, err := s.Registry.Load()
	if err != nil {
		return Definition{}, err
	}
	for _, def := range defs {
		if def.ID == id {
			return def, nil
		}
	}
	return Definition{}, newAutomationStatusError(http.StatusNotFound, "not_found", "automation not found")
}

func (s *Service) ReloadAutomations() error {
	if s.Orchestrator == nil {
		return nil
	}
	if err := s.Orchestrator.Reload(); err != nil {
		return err
	}
	return nil
}

func (s *Service) MapAutomationSummary(def Definition, next *time.Time) (api.AutomationSummaryResponse, error) {
	resp := api.AutomationSummaryResponse{
		ID:          def.ID,
		Name:        def.Name,
		Description: def.Description,
		Cron:        def.Cron,
		AgentKey:    def.AgentKey,
		Enabled:     def.Enabled,

		ZoneID:        def.Environment.ZoneID,
		SourceFile:    def.SourceFile,
		RemainingRuns: cloneIntPtr(def.RemainingRuns),
	}
	if next != nil && !next.IsZero() {
		nextFireAt := next.UnixMilli()
		if err := timecontract.ValidateEpochMillis(nextFireAt, "nextFireAt", "automation.nextFire"); err != nil {
			return api.AutomationSummaryResponse{}, err
		}
		// nextFireAt remains the authoritative instant. nextFireTime is a
		// second-precision display value in the platform timezone.
		formatted := automationReadableTimeMillis(nextFireAt, s.AutomationDisplayLocation())
		resp.NextFireAt = &nextFireAt
		resp.NextFireTime = &formatted
	}
	if s.History != nil {
		status := s.History.Status()
		if status.Available {
			last, err := s.History.LastExecution(def.ID)
			if err != nil {
				log.Printf("[automation] load last execution failed automationID=%s err=%v", def.ID, err)
			} else if last != nil {
				resp.LastExecution = mapAutomationExecutionBrief(*last, s.AutomationDisplayLocation())
			}
		}
	}
	return resp, nil
}

func mapAutomationQuery(query Query) api.AutomationQueryResponse {
	return api.AutomationQueryResponse{
		AccessLevel: query.AccessLevel,
		Message:     query.Message,
		ChatID:      query.ChatID,
		Role:        query.Role,
		Hidden:      cloneAutomationBoolPtr(query.Hidden),
		Params:      contracts.CloneAnyMap(query.Params),
	}
}

func automationQueryFromRequest(req api.AutomationQueryRequest) Query {
	return Query{
		AccessLevel: strings.TrimSpace(req.AccessLevel),
		ChatID:      strings.TrimSpace(req.ChatID),
		Role:        strings.TrimSpace(req.Role),
		Hidden:      cloneAutomationBoolPtr(req.Hidden),
		Message:     req.Message,
		Params:      contracts.CloneAnyMap(req.Params),
	}
}

func applyAutomationUpdate(def *Definition, req api.UpdateAutomationRequest) {
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

	if req.ZoneID != nil {
		def.Environment.ZoneID = strings.TrimSpace(*req.ZoneID)
	}
	if req.Enabled != nil {
		def.Enabled = *req.Enabled
	}
	if req.RemainingRunsSet || req.RemainingRuns != nil {
		def.RemainingRuns = cloneIntPtr(req.RemainingRuns)
	}
	if req.Query != nil {
		def.Query.AccessLevel = strings.TrimSpace(req.Query.AccessLevel)
		def.Query.ChatID = strings.TrimSpace(req.Query.ChatID)
		def.Query.Role = strings.TrimSpace(req.Query.Role)
		def.Query.Hidden = cloneAutomationBoolPtr(req.Query.Hidden)
		def.Query.Message = req.Query.Message
		def.Query.Params = contracts.CloneAnyMap(req.Query.Params)
	}
}

func cloneAutomationBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mapAutomationExecutionBrief(item Execution, loc *time.Location) *api.AutomationExecutionBrief {
	resp := &api.AutomationExecutionBrief{
		ID:            item.ID,
		Status:        item.Status,
		ZoneID:        item.ZoneID,
		ChatID:        item.ChatID,
		RunID:         item.RunID,
		FinishReason:  item.FinishReason,
		HasResult:     strings.TrimSpace(item.ResultPreview) != "",
		ResultPreview: item.ResultPreview,
		StartedAt:     item.StartedAt,
		StartedTime:   automationReadableTimeMillis(item.StartedAt, loc),
		RunStartedAt:  cloneInt64Ptr(item.RunStartedAt),
		CompletedAt:   cloneInt64Ptr(item.CompletedAt),
		DurationMs:    cloneInt64Ptr(item.DurationMs),
		Error:         item.Error,
	}
	if item.CompletedAt != nil {
		resp.CompletedTime = automationReadableTimeMillis(*item.CompletedAt, loc)
	}
	return resp
}

func mapAutomationExecution(item Execution, loc *time.Location) api.AutomationExecutionResponse {
	resp := api.AutomationExecutionResponse{
		ID:             item.ID,
		AutomationID:   item.AutomationID,
		AutomationName: item.AutomationName,
		SourceFile:     item.SourceFile,
		AgentKey:       item.AgentKey,

		Status:        item.Status,
		Error:         item.Error,
		ZoneID:        item.ZoneID,
		ChatID:        item.ChatID,
		RunID:         item.RunID,
		FinishReason:  item.FinishReason,
		HasResult:     strings.TrimSpace(firstNonBlank(item.ResultContent, item.ResultPreview)) != "",
		ResultPreview: item.ResultPreview,
		StartedAt:     item.StartedAt,
		StartedTime:   automationReadableTimeMillis(item.StartedAt, loc),
		RunStartedAt:  cloneInt64Ptr(item.RunStartedAt),
		CompletedAt:   cloneInt64Ptr(item.CompletedAt),
		DurationMs:    cloneInt64Ptr(item.DurationMs),
	}
	if item.CompletedAt != nil {
		resp.CompletedTime = automationReadableTimeMillis(*item.CompletedAt, loc)
	}
	return resp
}

func (s *Service) AutomationDisplayLocation() *time.Location {
	if s == nil {
		return time.Local
	}
	return loadAutomationAPILocation("", s.DefaultZoneID)
}

func loadAutomationAPILocation(zoneID string, defaultZoneID string) *time.Location {
	if loc, err := loadAutomationAPILocationByID(zoneID); err == nil {
		return loc
	}
	if loc, err := loadAutomationAPILocationByID(defaultZoneID); err == nil {
		return loc
	}
	return time.Local
}

func loadAutomationAPILocationByID(zoneID string) (*time.Location, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return nil, errors.New("empty zoneId")
	}
	return time.LoadLocation(zoneID)
}

func automationReadableTimeMillis(ms int64, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	return time.UnixMilli(ms).In(loc).Format("2006-01-02 15:04:05")
}

func (s *Service) NextAutomationID(name string) (string, error) {
	base := automationSlug(name)
	existing := map[string]struct{}{}
	defs, err := s.Registry.Load()
	if err != nil {
		return "", err
	}
	for _, def := range defs {
		existing[def.ID] = struct{}{}
	}
	root := strings.TrimSpace(s.Registry.Root())
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

func (s *Service) CreateAutomation(req api.CreateAutomationRequest) (api.AutomationDetailResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	s.Registry.managementMu.Lock()
	defer s.Registry.managementMu.Unlock()
	return s.createAutomation(req)
}

func (s *Service) UpdateAutomation(req api.UpdateAutomationRequest) (api.AutomationDetailResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.AutomationDetailResponse{}, err
	}
	s.Registry.managementMu.Lock()
	defer s.Registry.managementMu.Unlock()
	return s.updateAutomation(req)
}

func (s *Service) DeleteAutomation(req api.DeleteAutomationRequest) (map[string]any, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return nil, err
	}
	s.Registry.managementMu.Lock()
	defer s.Registry.managementMu.Unlock()
	return s.deleteAutomation(req)
}

func (s *Service) TriggerAutomation(id string) (api.TriggerAutomationResponse, error) {
	if err := s.AutomationDepsReady(); err != nil {
		return api.TriggerAutomationResponse{}, err
	}
	s.Registry.managementMu.Lock()
	defer s.Registry.managementMu.Unlock()
	return s.triggerAutomation(id)
}
