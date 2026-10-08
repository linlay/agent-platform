package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/i18n"
	"agent-platform/internal/models"
)

// Creation defaults describe runtime facts; clients own their selection UI.
func (s *Server) handleAgentCreationDefaults(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.buildAgentCreationDefaults(responseLocale(w))))
}

func (s *Server) agentCreationModelAvailable(modelKey string) bool {
	if strings.TrimSpace(modelKey) == "" || s.deps.Models == nil {
		return false
	}
	model, err := s.deps.Models.GetModel(modelKey)
	return err == nil && models.IsChatModel(model)
}

func (s *Server) buildAgentCreationDefaults(locale string) api.AgentCreationDefaultsResponse {
	english := strings.HasPrefix(strings.ToLower(i18n.ResolveLocale(locale)), "en")
	label := func(zh string, en string) string {
		if english {
			return en
		}
		return zh
	}

	nativeType := func(typeKey string, mode string, labelText string, modelKey string, reasoningEffort string) api.AgentCreationTypeOption {
		baseTools := []string{}
		switch typeKey {
		case "general":
			baseTools = agentbuiltin.GeneralCreateToolNames()
		case "kbase":
			baseTools = agentbuiltin.KBaseCreateToolNames()
		}
		return api.AgentCreationTypeOption{
			Key: typeKey, Label: labelText, Mode: mode, Engine: catalog.AgentEngineNative,
			Available: true, WorkspaceRequired: true, ModelRequired: true,
			DefaultModelKey:        strings.TrimSpace(modelKey),
			DefaultModelAvailable:  s.agentCreationModelAvailable(modelKey),
			DefaultReasoningEffort: strings.TrimSpace(reasoningEffort),
			BaseTools:              baseTools,
		}
	}
	cfg := s.deps.Config
	coder := nativeType("coder", catalog.AgentModeCoder, label("编程智能体", "Coding agent"), cfg.CoderSettings.DefaultAgent.ModelKey, cfg.CoderSettings.DefaultAgent.ReasoningEffort)
	// Creation recommendations are persisted by the client as explicit tools.
	coder.BaseTools = agentbuiltin.CoderCreateToolNames()

	bridgeIDs := make([]string, 0, len(cfg.ACP.ACPBridges))
	for id := range cfg.ACP.ACPBridges {
		bridgeIDs = append(bridgeIDs, id)
	}
	sort.Strings(bridgeIDs)
	acp := api.AgentCreationTypeOption{
		Key: "acp", Label: label("外部引擎", "External engine"),
		Mode: catalog.AgentModeCoder, Engine: catalog.AgentEngineACP,
		Available: len(bridgeIDs) > 0, WorkspaceRequired: true,
		BaseTools: []string{},
	}
	if !acp.Available {
		acp.UnavailableReason = label("未在 configs/agent-settings.yml 配置 acp-bridges", "No acp-bridges are configured in configs/agent-settings.yml")
	}
	for _, id := range bridgeIDs {
		acp.ACPBridges = append(acp.ACPBridges, api.AgentCreationACPBridge{ID: id})
	}

	return api.AgentCreationDefaultsResponse{
		Types: []api.AgentCreationTypeOption{
			nativeType("general", catalog.AgentModeGeneral, label("通用智能体", "General agent"), cfg.GeneralSettings.DefaultAgent.ModelKey, cfg.GeneralSettings.DefaultAgent.ReasoningEffort),
			coder,
			nativeType("kbase", catalog.AgentModeKBase, label("知识库智能体", "Knowledge-base agent"), cfg.KBase.DefaultAgent.ModelKey, cfg.KBase.DefaultAgent.ReasoningEffort),
			acp,
		},
		Models: s.buildAgentEditorOptions().Models,
	}
}

// Validate a concrete model selection after applying the mode defaults.
func (s *Server) validateCreateModel(definition map[string]any) error {
	mode, engine, err := catalog.ParseAgentModeAndEngine(stringValue(definition["mode"]), stringValue(definition["engine"]))
	if err != nil {
		return err
	}
	if engine == catalog.AgentEngineACP || (mode != catalog.AgentModeGeneral && mode != catalog.AgentModeCoder && mode != catalog.AgentModeKBase) {
		return nil
	}
	modelConfig, _ := definition["modelConfig"].(map[string]any)
	runtimeConfig, _ := definition["runtimeConfig"].(map[string]any)
	key := strings.TrimSpace(stringValue(modelConfig["modelKey"]))
	if key == "" {
		if strings.TrimSpace(stringValue(runtimeConfig["workspaceRoot"])) != "" {
			return newAgentStatusError(http.StatusBadRequest, "model_required", "modelConfig.modelKey is required: choose a model or configure a default model")
		}
		return nil
	}
	if !s.agentCreationModelAvailable(key) {
		return newAgentStatusError(http.StatusBadRequest, "model_unavailable", fmt.Sprintf("model %q is not an available chat model", key))
	}
	return nil
}
