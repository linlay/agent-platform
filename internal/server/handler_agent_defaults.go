package server

import (
	"fmt"
	"net/http"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/i18n"
	"agent-platform/internal/models"
)

// Creation defaults describe runtime facts; clients own their selection UI.
func (s *Server) handleAgentCreationDefaults(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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

	acp := api.AgentCreationTypeOption{Key: "acp", Label: label("外部引擎", "External engine"), Engine: catalog.AgentEngineACP, WorkspaceRequired: true, BaseTools: []string{}}
	if s.deps.AgentEngines == nil || s.deps.AgentEngines.Status().HostID == "" {
		acp.UnavailableCode = "unsupported_deployment"
		acp.UnavailableReason = label("此部署不支持本机引擎插件", "This deployment does not support local engine plugins")
	} else {
		directory := s.deps.AgentEngines.Directory()
		acp.EngineDirectory = &directory
		acp.UnavailableCode = "engine_unavailable"
		acp.UnavailableReason = label("请在 Desktop 市场安装并启用引擎插件", "Install and enable an engine plugin in the Desktop marketplace")
		if s.deps.AgentEngines.IsStandalone() {
			acp.UnavailableReason = label("请由服务器管理员安装并启用引擎插件", "Ask the server administrator to install and enable an engine plugin")
		}
		if !directory.Ready {
			acp.UnavailableCode = "registry_not_ready"
			acp.UnavailableReason = label("引擎目录待同步", "Engine directory is awaiting synchronization")
		}
		for _, entry := range directory.Engines {
			if directory.Ready && entry.Enabled && entry.SyncState == "applied" && entry.DependencyState == "ready" {
				acp.Available = true
				acp.UnavailableReason = ""
				acp.UnavailableCode = ""
				break
			}
		}
	}

	return api.AgentCreationDefaultsResponse{
		Types: []api.AgentCreationTypeOption{
			nativeType("general", catalog.AgentModeGeneral, label("通用智能体", "General agent"), cfg.GeneralSettings.DefaultAgent.ModelKey, cfg.GeneralSettings.DefaultAgent.ReasoningEffort),
			nativeType("team", "TEAM", label("团队智能体", "Team agent"), cfg.TeamSettings.DefaultAgent.ModelKey, cfg.TeamSettings.DefaultAgent.ReasoningEffort),
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
	if engine == catalog.AgentEngineACP || (mode != catalog.AgentModeGeneral && mode != catalog.AgentModeCoder && mode != catalog.AgentModeKBase && mode != catalog.AgentModeExecutor) {
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
