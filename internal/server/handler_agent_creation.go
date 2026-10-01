package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/agentcreation"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	"agent-platform/internal/models"
	"agent-platform/internal/skillmeta"
)

const hostRootWorkspaceMarker = "@root"

func (s *Server) handleAgentCreationOptions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.buildAgentCreationOptions(responseLocale(w))))
}

func (s *Server) agentCreationTemplate() agentcreation.Template {
	return agentcreation.Template{
		Config: s.deps.Config.AgentCreation,
		TypeTools: map[string][]string{
			agentcreation.TypeGeneral: agentbuiltin.GeneralCreateToolNames(),
			agentcreation.TypeKBase:   agentbuiltin.KBaseCreateToolNames(),
		},
	}
}

func (s *Server) agentCreationLookup() agentcreation.Lookup {
	toolNames := map[string]bool{}
	if s.deps.Tools != nil {
		for _, tool := range s.deps.Tools.Definitions() {
			toolNames[strings.TrimSpace(tool.Name)] = true
		}
	}
	sources := s.connectorSources()
	return agentcreation.Lookup{
		SkillExists: func(key string) bool {
			if s.deps.Registry == nil {
				return false
			}
			_, ok := s.deps.Registry.SkillDefinition(key)
			return ok
		},
		ToolExists: func(name string) bool { return toolNames[name] },
		ConnectorExists: func(id string) bool {
			_, err := sources.Load(id)
			return err == nil
		},
		ConnectorConflict: func(ids []string) error {
			packages := make([]connector.Package, 0, len(ids))
			for _, id := range ids {
				pkg, err := sources.Load(id)
				if err != nil {
					return err
				}
				packages = append(packages, pkg)
			}
			return connector.ValidateSelection(packages)
		},
	}
}

// agentCreationType maps a creation definition to its template type.
func agentCreationType(definition map[string]any) (string, error) {
	mode, engine, err := catalog.ParseAgentModeAndEngine(stringValue(definition["mode"]), stringValue(definition["engine"]))
	if err != nil {
		return "", err
	}
	switch {
	case engine == catalog.AgentEngineACP:
		return agentcreation.TypeACP, nil
	case mode == catalog.AgentModeGeneral:
		return agentcreation.TypeGeneral, nil
	case mode == catalog.AgentModeCoder:
		return agentcreation.TypeCoder, nil
	case mode == catalog.AgentModeKBase:
		return agentcreation.TypeKBase, nil
	default:
		return "", fmt.Errorf("capabilityGroups is not supported for mode %s", catalog.AgentModeForAPI(mode))
	}
}

// applyAgentCreationTemplate turns a project-creation request into a concrete
// definition. Everything is checked before anything is written to disk so a
// rejected request leaves no agent directory behind.
func (s *Server) applyAgentCreationTemplate(definition map[string]any, groupKeys []string) (map[string]any, error) {
	typeKey, err := agentCreationType(definition)
	if err != nil {
		return nil, newAgentStatusError(http.StatusBadRequest, agentcreation.CodeGroupsUnsupported, err.Error())
	}
	workspaceRoot := strings.TrimSpace(contracts.AnyStringNode(contracts.AnyMapNode(definition["runtimeConfig"])["workspaceRoot"]))
	if workspaceRoot == "" || workspaceRoot == hostRootWorkspaceMarker {
		return nil, newAgentStatusError(http.StatusBadRequest, "workspace_required", "a project directory is required: set runtimeConfig.workspaceRoot to a specific directory")
	}
	template := s.agentCreationTemplate()
	expansion, err := template.Expand(typeKey, groupKeys, s.agentCreationLookup())
	if err != nil {
		var creationErr *agentcreation.Error
		if errors.As(err, &creationErr) {
			return nil, newAgentStatusError(http.StatusBadRequest, creationErr.Code, creationErr.Message)
		}
		return nil, newAgentStatusError(http.StatusBadRequest, "invalid_agent_definition", err.Error())
	}
	if typeKey == agentcreation.TypeACP {
		return definition, nil
	}
	modelKey := strings.TrimSpace(contracts.AnyStringNode(contracts.AnyMapNode(definition["modelConfig"])["modelKey"]))
	if modelKey == "" {
		return nil, newAgentStatusError(http.StatusBadRequest, "model_required", "no default model is configured for this agent type; choose a model")
	}
	if !s.agentCreationModelAvailable(modelKey) {
		return nil, newAgentStatusError(http.StatusBadRequest, "model_unavailable", fmt.Sprintf("model %q is not an available chat model; choose another model", modelKey))
	}
	var modeDefaultTools []string
	if typeKey == agentcreation.TypeCoder {
		modeDefaultTools = agentbuiltin.CoderDefaultToolNames()
	}
	return agentcreation.ApplyToDefinition(definition, template.BaseTools(typeKey), expansion, modeDefaultTools), nil
}

func (s *Server) agentCreationModelAvailable(modelKey string) bool {
	if strings.TrimSpace(modelKey) == "" || s.deps.Models == nil {
		return false
	}
	model, err := s.deps.Models.GetModel(modelKey)
	return err == nil && models.IsChatModel(model)
}

func (s *Server) buildAgentCreationOptions(locale string) api.AgentCreationOptionsResponse {
	template := s.agentCreationTemplate()
	lookup := s.agentCreationLookup()
	english := strings.HasPrefix(strings.ToLower(i18n.ResolveLocale(locale)), "en")
	label := func(zh string, en string) string {
		if english {
			return en
		}
		return zh
	}

	groups := make([]api.AgentCreationGroupOption, 0, len(template.Config.Groups))
	availableGroups := map[string]bool{}
	for _, group := range template.Config.Groups {
		option := api.AgentCreationGroupOption{
			Key:         group.Key,
			Name:        firstNonBlank(agentcreation.LocalizedText(group.Name, locale), group.Key),
			Description: agentcreation.LocalizedText(group.Description, locale),
			Skills:      make([]api.AgentCreationMember, 0, len(group.Skills)),
			Tools:       append([]string{}, group.Tools...),
			Connectors:  make([]api.AgentCreationMember, 0, len(group.Connectors)),
			Available:   true,
		}
		for _, key := range group.Skills {
			option.Skills = append(option.Skills, api.AgentCreationMember{Key: key, Name: s.agentCreationSkillName(key, locale)})
		}
		for _, id := range group.Connectors {
			option.Connectors = append(option.Connectors, api.AgentCreationMember{Key: id, Name: s.agentCreationConnectorName(id, locale)})
		}
		if missing := agentcreation.MissingMembers(group, lookup); len(missing) > 0 {
			option.Available = false
			option.UnavailableReason = "missing " + strings.Join(missing, ", ")
		} else if lookup.ConnectorConflict != nil && len(group.Connectors) > 1 {
			if err := lookup.ConnectorConflict(group.Connectors); err != nil {
				option.Available = false
				option.UnavailableReason = err.Error()
			}
		}
		availableGroups[group.Key] = option.Available
		groups = append(groups, option)
	}

	nativeType := func(typeKey string, mode string, labelText string, modelKey string, reasoningEffort string) api.AgentCreationTypeOption {
		defaults := make([]string, 0)
		for _, key := range template.DefaultGroups(typeKey) {
			if availableGroups[key] {
				defaults = append(defaults, key)
			}
		}
		baseTools := template.BaseTools(typeKey)
		if baseTools == nil {
			baseTools = []string{}
		}
		return api.AgentCreationTypeOption{
			Key: typeKey, Label: labelText, Mode: mode, Engine: catalog.AgentEngineNative,
			Available: true, WorkspaceRequired: true, ModelRequired: true,
			DefaultModelKey:        strings.TrimSpace(modelKey),
			DefaultModelAvailable:  s.agentCreationModelAvailable(modelKey),
			DefaultReasoningEffort: strings.TrimSpace(reasoningEffort),
			SupportsGroups:         true,
			BaseTools:              baseTools,
			DefaultGroups:          defaults,
		}
	}
	cfg := s.deps.Config
	coder := nativeType(agentcreation.TypeCoder, catalog.AgentModeCoder, label("编程智能体", "Coding agent"), cfg.CoderSettings.DefaultAgent.ModelKey, cfg.CoderSettings.DefaultAgent.ReasoningEffort)
	// CODER tools come from its built-in default when agent.yml declares none.
	coder.BaseTools = agentbuiltin.CoderDefaultToolNames()

	bridgeIDs := make([]string, 0, len(cfg.CoderSettings.ACPBridges))
	for id := range cfg.CoderSettings.ACPBridges {
		bridgeIDs = append(bridgeIDs, id)
	}
	sort.Strings(bridgeIDs)
	acp := api.AgentCreationTypeOption{
		Key: agentcreation.TypeACP, Label: label("外部引擎", "External engine"),
		Mode: catalog.AgentModeCoder, Engine: catalog.AgentEngineACP,
		Available: len(bridgeIDs) > 0, WorkspaceRequired: true,
		GroupsUnsupportedReason: label("外部引擎自行管理能力，不使用平台的技能、工具和连接器", "The external engine manages its own capabilities and does not use platform skills, tools or connectors"),
		BaseTools:               []string{},
		DefaultGroups:           []string{},
	}
	if !acp.Available {
		acp.UnavailableReason = label("未在 configs/coder-settings.yml 配置 acp-bridges", "No acp-bridges are configured in configs/coder-settings.yml")
	}
	for _, id := range bridgeIDs {
		acp.ACPBridges = append(acp.ACPBridges, api.AgentCreationACPBridge{ID: id})
	}

	return api.AgentCreationOptionsResponse{
		Types: []api.AgentCreationTypeOption{
			nativeType(agentcreation.TypeGeneral, catalog.AgentModeGeneral, label("通用智能体", "General agent"), cfg.GeneralSettings.DefaultAgent.ModelKey, cfg.GeneralSettings.DefaultAgent.ReasoningEffort),
			coder,
			nativeType(agentcreation.TypeKBase, catalog.AgentModeKBase, label("知识库智能体", "Knowledge-base agent"), cfg.KBase.DefaultAgent.ModelKey, cfg.KBase.DefaultAgent.ReasoningEffort),
			acp,
		},
		Groups: groups,
		Models: s.buildAgentEditorOptions().Models,
	}
}

func (s *Server) agentCreationSkillName(key string, locale string) string {
	if s.deps.Registry == nil {
		return key
	}
	skill, ok := s.deps.Registry.SkillDefinition(key)
	if !ok {
		return key
	}
	presentation, _ := skillmeta.Parse(skill.Metadata, skill.Version).Resolve(locale, skill.Name, key, skill.Description)
	return firstNonBlank(presentation.DisplayName, key)
}

func (s *Server) agentCreationConnectorName(id string, locale string) string {
	pkg, err := s.connectorSources().Load(id)
	if err != nil {
		return id
	}
	return firstNonBlank(pkg.Manifest.Localized(locale).Name, id)
}
