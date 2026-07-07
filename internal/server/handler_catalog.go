package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
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
	data    map[string]any
}

func (e agentStatusError) Error() string { return e.message }

func newAgentStatusError(status int, code string, message string) error {
	return agentStatusError{status: status, code: code, message: message}
}

func newAgentStatusErrorWithData(status int, code string, message string, data map[string]any) error {
	return agentStatusError{status: status, code: code, message: message, data: data}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	includeChats, ok := parseIncludeChats(w, r.URL.Query().Get("includeChats"))
	if !ok {
		return
	}
	scope, err := catalog.NormalizeAgentSummaryScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	items, err := s.listAgentSummaries(includeChats, scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(items))
}

const maxAgentSummaryIncludeChats = 50

func parseIncludeChats(w http.ResponseWriter, raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "includeChats must be an integer"))
		return 0, false
	}
	if value < 0 {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "includeChats must be greater than or equal to 0"))
		return 0, false
	}
	if value > maxAgentSummaryIncludeChats {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "includeChats must be less than or equal to 50"))
		return 0, false
	}
	return value, true
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
	response := s.buildAgentDetailResponse(def)
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
	key, err := queryOrBodyIDAny(r, []string{"agentKey", "key"}, req.AgentKey, req.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.Key = key
	response, err := s.updateAgent(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentUpdateName(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateAgentNameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	key, err := queryOrBodyIDAny(r, []string{"agentKey", "key"}, req.AgentKey, req.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "name is required"))
		return
	}
	response, err := s.updateAgentName(r.Context(), key, name)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentModelConfig(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateAgentModelConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	key, err := queryOrBodyIDAny(r, []string{"agentKey", "key"}, req.AgentKey, req.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.Key = key
	response, err := s.updateAgentModelConfig(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAgentRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	key, err := queryOrBodyIDAny(r, []string{"agentKey", "key"}, req.AgentKey, req.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	req.Key = key
	response, err := s.deleteAgent(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentOpenWorkspace(w http.ResponseWriter, r *http.Request) {
	var req api.OpenAgentWorkspaceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.openAgentWorkspace(req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAgentEditorOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.buildAgentEditorOptions()))
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Teams()))
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.deps.Registry.(adminSkillRegistry); ok {
		response, err := s.listAdminSkills()
		s.writeAgentHTTPResponse(w, response, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Skills("")))
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.listTools()))
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
	if err := validateCreateAgentDefinition(req.Definition); err != nil {
		return api.AgentDetailResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_agent_definition", err.Error())
	}
	key := strings.TrimSpace(req.Key)
	definition := s.applyCreateDefaultAgentConfig(req.Definition)
	key, definition = s.normalizeGeneratedModeCreation(key, definition)
	if _, err := editor.CreateEditableAgent(key, definition, req.SoulPrompt, req.AgentsPrompt); err != nil {
		return api.AgentDetailResponse{}, mapAgentEditError(err)
	}
	return s.reloadAndLoadAgent(ctx, key)
}

func validateCreateAgentDefinition(definition map[string]any) error {
	mode := catalog.NormalizeAgentModeForRuntime(stringValue(definition["mode"]))
	if mode != catalog.AgentModeKBase {
		return nil
	}
	kbaseConfig := contracts.AnyMapNode(definition["kbaseConfig"])
	embedding := contracts.AnyMapNode(kbaseConfig["embedding"])
	for _, key := range []string{"providerKey", "model", "dimension", "timeout"} {
		if _, exists := embedding[key]; exists {
			return fmt.Errorf("kbaseConfig.embedding.%s is no longer supported; use kbaseConfig.embedding.modelKey", key)
		}
	}
	chunk := contracts.AnyMapNode(kbaseConfig["chunk"])
	if rawUnit, exists := chunk["unit"]; exists {
		unit := strings.TrimSpace(stringValue(rawUnit))
		if _, ok := catalog.NormalizeAgentKBaseChunkUnit(unit); !ok {
			return fmt.Errorf("kbaseConfig.chunk.unit must be estimatedTokens or chars")
		}
	}
	return nil
}

func (s *Server) applyCreateDefaultAgentConfig(definition map[string]any) map[string]any {
	definition = s.applyCoderDefaultAgentConfig(definition)
	definition = s.applyKBaseDefaultAgentConfig(definition)
	return definition
}

func (s *Server) applyCoderDefaultAgentConfig(definition map[string]any) map[string]any {
	if definition == nil {
		return nil
	}
	mode := catalog.NormalizeAgentModeForRuntime(stringValue(definition["mode"]))
	if mode != catalog.AgentModeCoder {
		return definition
	}
	defaults := s.deps.Config.CoderSettings.DefaultAgent
	modelKey := strings.TrimSpace(defaults.ModelKey)
	reasoningEffort := strings.TrimSpace(defaults.ReasoningEffort)
	budget := defaults.Budget
	if modelKey == "" && reasoningEffort == "" && len(budget) == 0 {
		return definition
	}

	out := contracts.CloneMap(definition)
	if _, exists := out["budget"]; !exists && len(budget) > 0 {
		out["budget"] = contracts.CloneMap(budget)
	}
	modelConfig := contracts.CloneMap(contracts.AnyMapNode(out["modelConfig"]))
	if modelConfig == nil {
		modelConfig = map[string]any{}
	}
	if modelKey != "" && strings.TrimSpace(stringValue(modelConfig["modelKey"])) == "" {
		modelConfig["modelKey"] = modelKey
	}
	if reasoningEffort != "" {
		reasoning := contracts.CloneMap(contracts.AnyMapNode(modelConfig["reasoning"]))
		if reasoning == nil {
			reasoning = map[string]any{}
		}
		if strings.TrimSpace(stringValue(reasoning["effort"])) == "" {
			reasoning["effort"] = reasoningEffort
		}
		if len(reasoning) > 0 {
			modelConfig["reasoning"] = reasoning
		}
	}
	if len(modelConfig) > 0 {
		out["modelConfig"] = modelConfig
	}
	return out
}

func (s *Server) applyKBaseDefaultAgentConfig(definition map[string]any) map[string]any {
	if definition == nil {
		return nil
	}
	mode := catalog.NormalizeAgentModeForRuntime(stringValue(definition["mode"]))
	if mode != catalog.AgentModeKBase {
		return definition
	}
	defaults := s.deps.Config.KBase.DefaultAgent
	modelKey := strings.TrimSpace(defaults.ModelKey)
	reasoningEffort := strings.TrimSpace(defaults.ReasoningEffort)
	embeddingDefaults := s.deps.Config.KBase.Embedding
	embeddingModelKey := strings.TrimSpace(embeddingDefaults.ModelKey)

	out := contracts.CloneMap(definition)
	if isEmptyDefinitionValue(out["icon"]) {
		out["icon"] = map[string]any{"name": catalog.DefaultKBaseAgentIconName}
	}
	visibility := contracts.CloneMap(contracts.AnyMapNode(out["visibility"]))
	if visibility == nil {
		visibility = map[string]any{}
	}
	if !hasNonBlankStringList(visibility["scopes"]) {
		visibility["scopes"] = []any{"nav"}
		out["visibility"] = visibility
	}
	if modelKey != "" || reasoningEffort != "" {
		modelConfig := contracts.CloneMap(contracts.AnyMapNode(out["modelConfig"]))
		if modelConfig == nil {
			modelConfig = map[string]any{}
		}
		if modelKey != "" && strings.TrimSpace(stringValue(modelConfig["modelKey"])) == "" {
			modelConfig["modelKey"] = modelKey
		}
		if reasoningEffort != "" {
			reasoning := contracts.CloneMap(contracts.AnyMapNode(modelConfig["reasoning"]))
			if reasoning == nil {
				reasoning = map[string]any{}
			}
			if strings.TrimSpace(stringValue(reasoning["effort"])) == "" {
				reasoning["effort"] = reasoningEffort
			}
			if len(reasoning) > 0 {
				modelConfig["reasoning"] = reasoning
			}
		}
		if len(modelConfig) > 0 {
			out["modelConfig"] = modelConfig
		}
	}
	kbaseConfig := contracts.CloneMap(contracts.AnyMapNode(out["kbaseConfig"]))
	embedding := contracts.CloneMap(contracts.AnyMapNode(kbaseConfig["embedding"]))
	explicitEmbeddingModelKey := strings.TrimSpace(stringValue(embedding["modelKey"]))
	if explicitEmbeddingModelKey != "" || embeddingModelKey != "" {
		if kbaseConfig == nil {
			kbaseConfig = map[string]any{}
		}
		if explicitEmbeddingModelKey == "" {
			explicitEmbeddingModelKey = embeddingModelKey
		}
		if embedding == nil {
			embedding = map[string]any{}
		}
		embedding["modelKey"] = explicitEmbeddingModelKey
		kbaseConfig["embedding"] = embedding
		if len(kbaseConfig) > 0 {
			out["kbaseConfig"] = kbaseConfig
		}
	}
	return out
}

func isEmptyDefinitionValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func hasNonBlankStringList(value any) bool {
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if strings.TrimSpace(stringValue(item)) != "" {
				return true
			}
		}
	case []string:
		for _, item := range items {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
	}
	return false
}

func (s *Server) normalizeGeneratedModeCreation(key string, definition map[string]any) (string, map[string]any) {
	if definition == nil {
		return key, definition
	}
	mode := catalog.NormalizeAgentModeForRuntime(stringValue(definition["mode"]))
	prefix := ""
	switch mode {
	case catalog.AgentModeCoder:
		prefix = "coder"
	case catalog.AgentModeKBase:
		prefix = "kbase"
	default:
		return key, definition
	}
	newKey := prefix + "-" + strconv.FormatInt(time.Now().Unix(), 36)
	out := contracts.CloneMap(definition)
	out["key"] = newKey

	runtimeCfg := contracts.AnyMapNode(out["runtimeConfig"])
	wsRoot := strings.TrimSpace(stringValue(runtimeCfg["workspaceRoot"]))
	if wsRoot != "" {
		existingName := strings.TrimSpace(stringValue(out["name"]))
		if existingName == "" {
			out["name"] = filepath.Base(wsRoot)
		}
	}
	return newKey, out
}

func (s *Server) updateAgent(ctx context.Context, req api.UpdateAgentRequest) (api.AgentDetailResponse, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	key := firstNonBlank(req.Key, req.AgentKey)
	if _, err := editor.UpdateEditableAgent(key, req.Definition, req.SoulPrompt, req.AgentsPrompt); err != nil {
		return api.AgentDetailResponse{}, mapAgentEditError(err)
	}
	return s.reloadAndLoadAgent(ctx, key)
}

func (s *Server) updateAgentName(ctx context.Context, key string, name string) (api.AgentDetailResponse, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	files, found, err := editor.EditableAgent(key)
	if err != nil {
		return api.AgentDetailResponse{}, mapAgentEditError(err)
	}
	if !found {
		return api.AgentDetailResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "agent not found")
	}
	definition := contracts.CloneMap(files.Definition)
	definition["name"] = name
	if _, err := editor.UpdateEditableAgent(key, definition, &files.SoulPrompt, &files.AgentsPrompt); err != nil {
		return api.AgentDetailResponse{}, mapAgentEditError(err)
	}
	return s.reloadAndLoadAgent(ctx, key)
}

func (s *Server) updateAgentModelConfig(ctx context.Context, req api.UpdateAgentModelConfigRequest) (api.AgentModelConfigResponse, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return api.AgentModelConfigResponse{}, err
	}
	key := firstNonBlank(req.Key, req.AgentKey)
	key = strings.TrimSpace(key)
	if key == "" {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey is required")
	}
	modelKey := strings.TrimSpace(req.ModelKey)
	if modelKey == "" {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "modelKey is required")
	}
	reasoningEffort, ok := normalizeCoderReasoningEffort(req.ReasoningEffort)
	if !ok {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "reasoningEffort must be NONE, LOW, MEDIUM, or HIGH")
	}
	serviceTier, ok := normalizeQueryModelServiceTier(req.ServiceTier)
	if !ok {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "serviceTier must be a non-empty string")
	}
	files, found, err := editor.EditableAgent(key)
	if err != nil {
		return api.AgentModelConfigResponse{}, mapAgentEditError(err)
	}
	if !found {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "editable agent not found")
	}
	if files.Source.Path == "" || files.Source.Kind == "" {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "editable agent source is missing")
	}
	def, ok := s.deps.Registry.AgentDefinition(key)
	if !ok {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "agent not found")
	}
	if !agentcoder.IsMode(def.Mode) {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agent model config can only be updated for CODER agents")
	}
	isACPCoder := catalog.AgentUsesACPCoderBackend(def)
	if serviceTier != "" && !isACPCoder {
		return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "serviceTier is only supported for ACP CODER")
	}
	if isACPCoder {
		options, err, ok := s.listACPCoderModelOptions(def.Key)
		if ok {
			if err != nil {
				return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadGateway, "upstream_error", "failed to fetch ACP CODER models: "+err.Error())
			}
			if !agentcoder.ModelKeyInOptions(modelKey, options) {
				return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "model "+modelKey+" is not available for ACP CODER")
			}
			if serviceTier != "" && !serviceTierAllowedForACPModel(serviceTier, modelKey, options) {
				return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "serviceTier "+serviceTier+" is not available for ACP CODER model "+modelKey)
			}
		} else {
			if s.deps.Models == nil {
				return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "model registry is not configured")
			}
			if err := s.validateLocalChatModelKey(modelKey, false); err != nil {
				return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
			}
		}
	} else {
		if s.deps.Models == nil {
			return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "model registry is not configured")
		}
		if err := s.validateLocalChatModelKey(modelKey, true); err != nil {
			return api.AgentModelConfigResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
		}
	}

	definition := contracts.CloneMap(files.Definition)
	modelConfig := contracts.CloneMap(contracts.AnyMapNode(definition["modelConfig"]))
	if modelConfig == nil {
		modelConfig = map[string]any{}
	}
	modelConfig["modelKey"] = modelKey
	reasoning := contracts.CloneMap(contracts.AnyMapNode(modelConfig["reasoning"]))
	if reasoning == nil {
		reasoning = map[string]any{}
	}
	if reasoningEffort == "NONE" {
		reasoning["enabled"] = false
		delete(reasoning, "effort")
	} else if reasoningEffort != "" {
		reasoning["enabled"] = true
		reasoning["effort"] = reasoningEffort
	}
	if len(reasoning) > 0 {
		modelConfig["reasoning"] = reasoning
	}
	if isACPCoder && serviceTier != "" {
		modelConfig["serviceTier"] = serviceTier
	} else {
		delete(modelConfig, "serviceTier")
	}
	definition["modelConfig"] = modelConfig

	if _, err := editor.UpdateEditableAgent(key, definition, &files.SoulPrompt, &files.AgentsPrompt); err != nil {
		return api.AgentModelConfigResponse{}, mapAgentEditError(err)
	}
	if s.deps.Registry != nil {
		if err := s.deps.Registry.Reload(ctx, "agents"); err != nil {
			return api.AgentModelConfigResponse{}, err
		}
	}
	return api.AgentModelConfigResponse{
		Key:         key,
		ModelConfig: modelConfig,
	}, nil
}

func (s *Server) deleteAgent(ctx context.Context, req api.DeleteAgentRequest) (map[string]any, error) {
	editor, err := s.agentEditor()
	if err != nil {
		return nil, err
	}
	key := firstNonBlank(req.Key, req.AgentKey)
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

func (s *Server) openAgentWorkspace(req api.OpenAgentWorkspaceRequest) (api.OpenAgentWorkspaceResponse, error) {
	agentKey := firstNonBlank(req.AgentKey, req.Key)
	workspaceDir, err := s.resolveOpenWorkspacePath(agentKey, req.WorkspaceDir)
	if err != nil {
		return api.OpenAgentWorkspaceResponse{}, err
	}
	if err := openWorkspacePath(workspaceDir); err != nil {
		return api.OpenAgentWorkspaceResponse{}, newAgentStatusError(http.StatusInternalServerError, "open_failed", err.Error())
	}
	return api.OpenAgentWorkspaceResponse{
		AgentKey:     agentKey,
		WorkspaceDir: workspaceDir,
		Opened:       true,
	}, nil
}

func (s *Server) resolveOpenWorkspacePath(agentKey string, requestedWorkspaceDir string) (string, error) {
	if s.deps.Registry == nil {
		return "", newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent registry is not configured")
	}
	agentKey = strings.TrimSpace(agentKey)
	requestedWorkspaceDir = strings.TrimSpace(requestedWorkspaceDir)
	if agentKey == "" && requestedWorkspaceDir == "" {
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey or workspaceDir is required")
	}
	if agentKey != "" {
		def, ok := s.deps.Registry.AgentDefinition(agentKey)
		if !ok {
			return "", newAgentStatusError(http.StatusNotFound, "not_found", "agent not found")
		}
		return validatedWorkspaceDir(def.Workspace.Root)
	}
	for _, item := range s.deps.Registry.Agents("all") {
		if samePath(item.WorkspaceDir, requestedWorkspaceDir) {
			return validatedWorkspaceDir(item.WorkspaceDir)
		}
	}
	return "", newAgentStatusError(http.StatusForbidden, "forbidden", "workspaceDir must match a registered agent workspace")
}

func (s *Server) buildAgentEditorOptions() api.AgentEditorOptionsResponse {
	models := []api.AgentEditorModelOption{}
	if s.deps.Models != nil {
		for _, model := range s.deps.Models.List() {
			models = append(models, api.AgentEditorModelOption{
				Key:           model.Key,
				Name:          model.Name,
				Provider:      model.Provider,
				ModelID:       model.ModelID,
				Protocol:      model.Protocol,
				IsVision:      model.IsVision,
				ContextWindow: model.ContextWindow,
				Timeout:       model.Timeout,
			})
		}
	}
	return api.AgentEditorOptionsResponse{
		Models: models,
		ContextTags: []api.AgentEditorOption{
			{Key: "system", Label: "system"},
			{Key: "session", Label: "session"},
			{Key: "owner", Label: "owner"},
			{Key: "agents", Label: "agents"},
		},
		VisibilityScopes: []api.AgentEditorOption{
			{Key: "nav", Label: "nav"},
			{Key: "copilot", Label: "copilot"},
			{Key: "invoke", Label: "invoke"},
			{Key: "internal", Label: "internal"},
		},
		Modes: []api.AgentEditorOption{
			{Key: "REACT", Label: "REACT"},
			{Key: "PLAN-EXECUTE", Label: "PLAN-EXECUTE"},
			{Key: "CODER", Label: "CODER"},
			{Key: "CHANNEL", Label: "CHANNEL"},
			{Key: "PROXY", Label: "PROXY"},
		},
		ProxyConfigSchema: api.AgentEditorProxyConfigSchema{
			DefaultTimeout: 300,
			Fields: []api.AgentEditorProxyConfigField{
				{Key: "baseUrl", Label: "Base URL", Type: "string", Required: true},
				{Key: "transport", Label: "Transport (default ws)", Type: "select"},
				{Key: "agentKey", Label: "Upstream Agent Key", Type: "string"},
				{Key: "chatId", Label: "Upstream Chat ID", Type: "string"},
				{Key: "token", Label: "Token", Type: "password"},
				{Key: "timeout", Label: "Timeout (s)", Type: "number"},
			},
		},
		ChannelConfigSchema: api.AgentEditorChannelConfigSchema{
			ImportFields: []api.AgentEditorProxyConfigField{
				{Key: "channelId", Label: "Channel ID", Type: "string", Required: true},
				{Key: "remoteAgentKey", Label: "Remote Agent Key", Type: "string", Required: true},
			},
			ExportFields: []api.AgentEditorProxyConfigField{
				{Key: "channelId", Label: "Channel ID", Type: "string", Required: true},
				{Key: "externalAgentKey", Label: "External Agent Key", Type: "string"},
			},
			AllowFields: []api.AgentEditorProxyConfigField{
				{Key: "query", Label: "Query", Type: "boolean"},
				{Key: "submit", Label: "Submit", Type: "boolean"},
				{Key: "steer", Label: "Steer", Type: "boolean"},
				{Key: "interrupt", Label: "Interrupt", Type: "boolean"},
				{Key: "fileTransfer", Label: "File Transfer", Type: "boolean"},
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
	response.Definition = editableDefinitionForAPI(files.Definition)
	response.SoulPrompt = files.SoulPrompt
	response.AgentsPrompt = files.AgentsPrompt
	response.Source = &api.AgentSource{
		Kind:     files.Source.Kind,
		Path:     files.Source.Path,
		AgentDir: files.Source.AgentDir,
	}
}

func editableDefinitionForAPI(definition map[string]any) map[string]any {
	if definition == nil {
		return nil
	}
	out := make(map[string]any, len(definition))
	for key, value := range definition {
		out[key] = value
	}
	if _, ok := out["mode"]; ok {
		out["mode"] = catalog.AgentModeForAPI(stringValue(out["mode"]))
	}
	return out
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

var openWorkspacePath = defaultOpenWorkspacePath

func validatedWorkspaceDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", "agent workspace is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", newAgentStatusError(http.StatusNotFound, "not_found", "workspace directory not found")
		}
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if !info.IsDir() {
		return "", newAgentStatusError(http.StatusBadRequest, "invalid_request", "workspace path is not a directory")
	}
	return abs, nil
}

func samePath(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(strings.TrimSpace(left))
	rightAbs, rightErr := filepath.Abs(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func defaultOpenWorkspacePath(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func (s *Server) writeAgentHTTPResponse(w http.ResponseWriter, response any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	var statusErr agentStatusError
	if errors.As(err, &statusErr) {
		writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message, statusErr.data))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func (s *Server) wsAgentModelConfig(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.UpdateAgentModelConfigRequest](req)
	if err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	payload.Key = firstNonBlank(payload.AgentKey, payload.Key)
	response, updateErr := s.updateAgentModelConfig(ctx, payload)
	s.sendAgentWSResponse(conn, req, response, updateErr)
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
		conn.SendError(req.ID, statusErr.code, statusErr.status, statusErr.message, statusErr.data)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
	conn.CompleteRequest(req.ID)
}
