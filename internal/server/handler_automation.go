package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/ws"
)

func newAutomationStatusError(status int, code, message string) error {
	return automation.StatusError{Status: status, Code: code, Message: message}
}
func (s *Server) automationService() *automation.Service {
	if s.deps.AutomationService != nil {
		return s.deps.AutomationService
	}
	return &automation.Service{Registry: s.deps.AutomationRegistry, Orchestrator: s.deps.AutomationOrchestrator, History: s.deps.AutomationExecutions, DefaultZoneID: s.deps.Config.Automation.DefaultZoneID}
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

func (s *Server) handleAutomationTrigger(w http.ResponseWriter, r *http.Request) {
	var req api.TriggerAutomationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	id := firstNonBlank(req.ID, req.AutomationID)
	response, err := s.triggerAutomation(id)
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

func (s *Server) handleAutomationExecution(w http.ResponseWriter, r *http.Request) {
	var req api.AutomationExecutionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.loadAutomationExecution(req)
	s.writeAutomationHTTPResponse(w, response, err)
}

func (s *Server) writeAutomationHTTPResponse(w http.ResponseWriter, response any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	var statusErr automation.StatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.Status, api.Failure(statusErr.Status, statusErr.Message))
		return
	}
	if isTimeContractViolation(err) {
		writeTimeContractViolation(w, err)
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) automationDepsReady() error { return s.automationService().AutomationDepsReady() }
func (s *Server) listAutomations(req api.AutomationListRequest) (api.AutomationListResponse, error) {
	return s.automationService().ListAutomations(req)
}
func (s *Server) loadAutomation(id string) (api.AutomationDetailResponse, error) {
	return s.automationService().LoadAutomation(id)
}
func (s *Server) createAutomation(req api.CreateAutomationRequest) (api.AutomationDetailResponse, error) {
	return s.automationService().CreateAutomation(req)
}
func (s *Server) updateAutomation(req api.UpdateAutomationRequest) (api.AutomationDetailResponse, error) {
	return s.automationService().UpdateAutomation(req)
}
func (s *Server) deleteAutomation(req api.DeleteAutomationRequest) (map[string]any, error) {
	return s.automationService().DeleteAutomation(req)
}
func (s *Server) toggleAutomation(req api.ToggleAutomationRequest) (api.AutomationDetailResponse, error) {
	return s.automationService().ToggleAutomation(req)
}
func (s *Server) triggerAutomation(id string) (api.TriggerAutomationResponse, error) {
	return s.automationService().TriggerAutomation(id)
}
func (s *Server) listAutomationExecutions(req api.AutomationExecutionsRequest) (api.AutomationExecutionListResponse, error) {
	return s.automationService().ListAutomationExecutions(req)
}
func (s *Server) loadAutomationExecution(req api.AutomationExecutionRequest) (api.AutomationExecutionDetailResponse, error) {
	return s.automationService().LoadAutomationExecution(req)
}
func (s *Server) automationExecutionHistoryStatus() api.AutomationExecutionHistoryStatus {
	return s.automationService().AutomationExecutionHistoryStatus()
}
func (s *Server) findAutomation(id string) (automation.Definition, error) {
	return s.automationService().FindAutomation(id)
}
func (s *Server) reloadAutomations() error { return s.automationService().ReloadAutomations() }
func (s *Server) mapAutomationSummary(def automation.Definition, next *time.Time) (api.AutomationSummaryResponse, error) {
	return s.automationService().MapAutomationSummary(def, next)
}
func (s *Server) automationDisplayLocation() *time.Location {
	return s.automationService().AutomationDisplayLocation()
}
func (s *Server) nextAutomationID(name string) (string, error) {
	return s.automationService().NextAutomationID(name)
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

func (s *Server) wsAutomationExecution(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.AutomationExecutionRequest](req)
	if err != nil {
		s.sendAutomationWSError(conn, req, newAutomationStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, loadErr := s.loadAutomationExecution(payload)
	s.sendAutomationWSResponse(conn, req, response, loadErr)
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
	var statusErr automation.StatusError
	if errors.As(err, &statusErr) {
		conn.SendError(req.ID, statusErr.Code, statusErr.Status, statusErr.Message, nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if isTimeContractViolation(err) {
		sendTimeContractViolation(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
	conn.CompleteRequest(req.ID)
}
