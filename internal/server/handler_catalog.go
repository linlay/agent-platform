package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/ws"
)

type editableAgentRegistry interface {
	EditableAgent(key string) (catalog.EditableAgentFiles, bool, error)
	CreateEditableAgent(key string, definition map[string]any, soulPrompt *string, agentsPrompt *string) (catalog.EditableAgentFiles, error)
	UpdateEditableAgent(key string, definition map[string]any, soulPrompt *string, agentsPrompt *string) (catalog.EditableAgentFiles, error)
	DeleteEditableAgent(key string) error
}

type agentStatusError struct {
	status  int
	code    string
	message string
}

func (e agentStatusError) Error() string { return e.message }

func newAgentStatusError(status int, code string, message string) error {
	return agentStatusError{status: status, code: code, message: message}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	items, err := s.listAgentSummaries(r.URL.Query().Get("tag"), r.URL.Query().Get("channel"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(items))
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey is required"))
		return
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "agent not found"))
		return
	}
	response, err := s.buildEditableAgentDetailResponse(def)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleAgentCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.createAgent(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.updateAgent(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAgent(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentEditorOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.buildAgentEditorOptions()))
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Teams()))
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Skills(r.URL.Query().Get("tag"))))
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.listTools(r.URL.Query().Get("kind"), r.URL.Query().Get("tag"))))
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.URL.Query().Get("toolName")
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "toolName is required"))
		return
	}
	tool, ok := s.lookupTool(toolName)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "tool not found"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(tool))
}

func (s *Server) agentEditor() (editableAgentRegistry, error) {
	editor, ok := s.deps.Registry.(editableAgentRegistry)
	if !ok || editor == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent editing is not configured")
	}
	return editor, nil
}

func (s *Server) createAgent(ctx context.Context, req api.CreateAgentRequest) (api.AgentDetailResponse, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	key := strings.TrimSpace(req.Key)
	if _, err := editor.CreateEditableAgent(key, req.Definition, req.SoulPrompt, req.AgentsPrompt); err != nil {
		return api.AgentDetailResponse{}, mapAgentEditError(err)
	}
	return s.reloadAndLoadAgent(ctx, key)
}

func (s *Server) updateAgent(ctx context.Context, req api.UpdateAgentRequest) (api.AgentDetailResponse, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	key := strings.TrimSpace(req.Key)
	if _, err := editor.UpdateEditableAgent(key, req.Definition, req.SoulPrompt, req.AgentsPrompt); err != nil {
		return api.AgentDetailResponse{}, mapAgentEditError(err)
	}
	return s.reloadAndLoadAgent(ctx, key)
}

func (s *Server) deleteAgent(ctx context.Context, req api.DeleteAgentRequest) (map[string]any, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.Key)
	if err := editor.DeleteEditableAgent(key); err != nil {
		return nil, mapAgentEditError(err)
	}
	if s.deps.Registry != nil {
		if err := s.deps.Registry.Reload(ctx, "agents"); err != nil {
			return nil, err
		}
	}
	return map[string]any{"key": key, "deleted": true}, nil
}

func (s *Server) buildAgentEditorOptions() api.AgentEditorOptionsResponse {
	models := []api.AgentEditorModelOption{}
	if s.deps.Models != nil {
		for _, model := range s.deps.Models.List() {
			models = append(models, api.AgentEditorModelOption{
				Key:           model.Key,
				Provider:      model.Provider,
				ModelID:       model.ModelID,
				Protocol:      model.Protocol,
				IsVision:      model.IsVision,
				ContextWindow: model.ContextWindow,
			})
		}
	}
	return api.AgentEditorOptionsResponse{
		Models: models,
		ContextTags: []api.AgentEditorOption{
			{Key: "system", Label: "system"},
			{Key: "context", Label: "context"},
			{Key: "owner", Label: "owner"},
			{Key: "auth", Label: "auth"},
			{Key: "all-agents", Label: "all-agents"},
			{Key: "memory", Label: "memory"},
		},
		Modes: []api.AgentEditorOption{
			{Key: "REACT", Label: "REACT"},
			{Key: "PLAN_EXECUTE", Label: "PLAN-EXECUTE"},
			{Key: "PROXY", Label: "ACP-PROXY"},
		},
		ProxyConfigSchema: api.AgentEditorProxyConfigSchema{
			DefaultTimeoutMs: 300000,
			Fields: []api.AgentEditorProxyConfigField{
				{Key: "baseUrl", Label: "Base URL", Type: "string", Required: true},
				{Key: "transport", Label: "Transport", Type: "select"},
				{Key: "agentKey", Label: "Upstream Agent Key", Type: "string"},
				{Key: "chatId", Label: "Upstream Chat ID", Type: "string"},
				{Key: "token", Label: "Token", Type: "password"},
				{Key: "timeoutMs", Label: "Timeout (ms)", Type: "number"},
			},
		},
	}
}

func (s *Server) reloadAndLoadAgent(ctx context.Context, key string) (api.AgentDetailResponse, error) {
	if s.deps.Registry != nil {
		if err := s.deps.Registry.Reload(ctx, "agents"); err != nil {
			return api.AgentDetailResponse{}, err
		}
	}
	def, ok := s.deps.Registry.AgentDefinition(key)
	if !ok {
		return api.AgentDetailResponse{}, newAgentStatusError(http.StatusInternalServerError, "internal_error", "agent did not reload")
	}
	response, err := s.buildEditableAgentDetailResponse(def)
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	return response, nil
}

func (s *Server) buildEditableAgentDetailResponse(def catalog.AgentDefinition) (api.AgentDetailResponse, error) {
	response := s.buildAgentDetailResponse(def)
	editor, ok := s.deps.Registry.(interface {
		EditableAgent(key string) (catalog.EditableAgentFiles, bool, error)
	})
	if !ok || editor == nil {
		return response, nil
	}
	files, found, err := editor.EditableAgent(def.Key)
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	if !found {
		return response, nil
	}
	applyEditableAgentFiles(&response, files)
	return response, nil
}

func applyEditableAgentFiles(response *api.AgentDetailResponse, files catalog.EditableAgentFiles) {
	if response == nil {
		return
	}
	response.Definition = files.Definition
	response.SoulPrompt = files.SoulPrompt
	response.AgentsPrompt = files.AgentsPrompt
	response.Source = &api.AgentSource{
		Kind:     files.Source.Kind,
		Path:     files.Source.Path,
		AgentDir: files.Source.AgentDir,
	}
}

func mapAgentEditError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "not found"):
		return newAgentStatusError(http.StatusNotFound, "not_found", message)
	case strings.Contains(message, "already exists"):
		return newAgentStatusError(http.StatusConflict, "conflict", message)
	default:
		return newAgentStatusError(http.StatusBadRequest, "invalid_request", message)
	}
}

func (s *Server) writeAgentHTTPResponse(w http.ResponseWriter, response any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	var statusErr agentStatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) wsAgentCreate(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.CreateAgentRequest](req)
	if err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, createErr := s.createAgent(ctx, payload)
	s.sendAgentWSResponse(conn, req, response, createErr)
}

func (s *Server) wsAgentUpdate(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.UpdateAgentRequest](req)
	if err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, updateErr := s.updateAgent(ctx, payload)
	s.sendAgentWSResponse(conn, req, response, updateErr)
}

func (s *Server) wsAgentDelete(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.DeleteAgentRequest](req)
	if err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, deleteErr := s.deleteAgent(ctx, payload)
	s.sendAgentWSResponse(conn, req, response, deleteErr)
}

func (s *Server) sendAgentWSResponse(conn *ws.Conn, req ws.RequestFrame, response any, err error) {
	if err != nil {
		s.sendAgentWSError(conn, req, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) sendAgentWSError(conn *ws.Conn, req ws.RequestFrame, err error) {
	var statusErr agentStatusError
	if errors.As(err, &statusErr) {
		conn.SendError(req.ID, statusErr.code, statusErr.status, statusErr.message, nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
	conn.CompleteRequest(req.ID)
}
