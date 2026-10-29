package session

import (
	"agent-platform/internal/accesspolicy"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/memory"
	"agent-platform/internal/plantasks"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/runenv"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/temppaths"
)

type Options struct {
	// Recovered runs cannot reconstruct their original in-memory script grants.
	DisableSkillScriptGrants bool
	// SkipInteractionPolicy exempts the query's own references from the
	// user-facing interactionConfig; the frozen snapshot is left unchanged.
	SkipInteractionPolicy  bool
	Created                bool
	SubTaskID              string
	Locale                 string
	IncludeHistory         bool
	IncludeMemory          bool
	AllowInvokeAgents      bool
	Identity               *contracts.AuthIdentity
	TeamHistoryAgentKey    string
	TeamCoordinatorHistory bool
}

func (s *Builder) BuildQuerySession(ctx context.Context, req runtimetypes.QueryCommand, summary chat.Summary, agentDef catalog.AgentDefinition, options Options) (contracts.QuerySession, error) {
	planningModeRequested := agentbuiltin.PlanningModeEnabled(agentDef.Mode, req.PlanningMode != nil && *req.PlanningMode)
	// A planning Run never mutates the Workspace; editing takes effect in the
	// Run that executes the confirmed plan, which keeps the request's editingMode.
	// A fixed library binding is explicit configuration, so it is checked for
	// every mode; no mode requires a binding by itself.
	if agentDef.KBaseConfig.LibraryID != "" && s.deps.ValidateKnowledge != nil {
		if err := s.deps.ValidateKnowledge(agentDef.KBaseConfig.LibraryID); err != nil {
			return contracts.QuerySession{}, err
		}
	}
	editingMode := agentbuiltin.WorkspaceEditingDefault(agentDef.Mode)
	if req.EditingMode != nil {
		editingMode = *req.EditingMode
	}
	editingMode = editingMode && !planningModeRequested
	runAccessRoots := contracts.RunAccessRoots{}
	if err := AddConnectorAccessRoots(&runAccessRoots, agentDef); err != nil {
		return contracts.QuerySession{}, err
	}
	if err := catalog.ValidateOrdinaryAgentTools(agentDef.Tools); err != nil {
		return contracts.QuerySession{}, err
	}
	historyMessages := []map[string]any(nil)
	if options.IncludeHistory && s.deps.Chats != nil {
		var historyErr error
		if options.TeamCoordinatorHistory {
			if reader, ok := s.deps.Chats.(chat.TeamCoordinatorHistoryReader); ok {
				historyMessages, historyErr = reader.LoadTeamCoordinatorRawMessages(req.ChatID, chat.DefaultHistoryRunWindow)
			} else {
				historyMessages, historyErr = s.deps.Chats.LoadRawMessages(req.ChatID, chat.DefaultHistoryRunWindow)
			}
		} else if strings.TrimSpace(options.TeamHistoryAgentKey) != "" {
			if reader, ok := s.deps.Chats.(chat.TeamHistoryReader); ok {
				historyMessages, historyErr = reader.LoadTeamMemberRawMessages(req.ChatID, chat.DefaultHistoryRunWindow, options.TeamHistoryAgentKey)
				historyMessages = ExcludeHistoryRun(historyMessages, req.RunID)
			} else {
				historyMessages, historyErr = s.deps.Chats.LoadRawMessages(req.ChatID, chat.DefaultHistoryRunWindow)
			}
		} else {
			historyMessages, historyErr = s.deps.Chats.LoadRawMessages(req.ChatID, chat.DefaultHistoryRunWindow)
		}
		if historyErr != nil {
			return contracts.QuerySession{}, historyErr
		}
	}

	principal := options.Identity
	if principal == nil {
		principal = req.Identity
		if principal == nil {
			principal = runtimetypes.IdentityFromContext(ctx)
		}
	}
	runtimeContext, err := s.BuildContext(ContextInput{
		AgentKey: req.AgentKey,

		Role:               req.Role,
		ChatID:             req.ChatID,
		ChatName:           summary.ChatName,
		Scene:              req.Scene,
		References:         req.References,
		Principal:          principal,
		Definition:         agentDef,
		ExposeSkillsCenter: agentDef.Interaction().Attachment.Skills,
	})
	if err != nil {
		return contracts.QuerySession{}, err
	}
	req.References = runtimeContext.References

	promptAppend := BuildPromptAppendConfig(s.deps.Config.Prompts, agentDef)
	skillCatalogPrompt := BuildSkillCatalogPrompt(agentDef, s.deps.Config.Paths.SkillsCenterDir, promptAppend)
	resolvedWorkspaceRoot := strings.TrimSpace(runtimeContext.LocalPaths.WorkspaceDir)
	// Project settings are explicit configuration and apply to every mode; an
	// ACP bridge owns its own project instructions.
	workspaceAgentsPrompt := ""
	if agentDef.Engine != catalog.AgentEngineACP {
		if err := agentbuiltin.ValidateWorkspaceGit(agentbuiltin.WorkspaceGitPolicy{
			WorkspaceRoot:  resolvedWorkspaceRoot,
			ExpectedBranch: agentDef.Project.Git.ExpectedBranch,
		}); err != nil {
			return contracts.QuerySession{}, err
		}
		workspaceAgents := s.deps.Config.WorkspaceAgentsForMode(agentDef.Mode)
		workspaceAgentsPrompt, err = agentbuiltin.LoadWorkspacePrompt(agentbuiltin.WorkspacePromptPolicy{
			AgentDir:                agentDef.RuntimeDir,
			WorkspaceRoot:           resolvedWorkspaceRoot,
			ProjectDir:              agentDef.Workspace.ProjectDir(),
			ProjectPromptFiles:      ProjectPromptFiles(agentDef.Project.PromptFiles),
			WorkspaceAgentsEnabled:  workspaceAgents.Enabled,
			WorkspaceAgentsFileName: workspaceAgents.File,
		})
		if err != nil {
			return contracts.QuerySession{}, err
		}
	}
	runtimeEnvOverrides, err := ResolveSkillRuntimeSettings(
		RuntimeAgentEnv(agentDef.Runtime["env"]),
		agentDef.RuntimeDir,
		s.deps.Config.Paths.SkillsCenterDir,
		agentDef.EffectiveSkills(),
		agentDef,
	)
	if err != nil {
		return contracts.QuerySession{}, err
	}
	log.Printf("[server][skill-runtime] agent=%s skills=%v runtimeEnvKeys=%v",
		agentDef.Key,
		agentDef.EffectiveSkills(),
		SortedStringKeys(runtimeEnvOverrides),
	)

	configuredToolNames, mcpServerKeys := SessionMCPTools(EffectiveAgentTools(agentDef), agentDef, s.deps.Config.MCP.ToolMode, func() []string {
		return McpToolNamesForServers(s.deps.Tools, agentDef.ConnectorMCPServers)
	})
	toolNames := BuildSessionToolNames(configuredToolNames, options.AllowInvokeAgents)
	toolNames = RuntimeModeToolNames(toolNames, s.deps.Config.RuntimeMode)
	if options.SubTaskID != "" {
		filtered := make([]string, 0, len(toolNames))
		for _, name := range toolNames {
			if !strings.EqualFold(strings.TrimSpace(name), "run_env") && !connector.IsPlatformRootTool(name) {
				filtered = append(filtered, name)
			}
		}
		toolNames = filtered
	}
	log.Printf("[server][session-tools] agent=%s mode=%s count=%d tools=%v", agentDef.Key, agentDef.Mode, len(toolNames), toolNames)
	capabilityPrompts := []string(nil)
	if prompt := knowledgeSelectionPrompt(agentDef.KBaseConfig.LibraryID, nil); prompt != "" {
		capabilityPrompts = append(capabilityPrompts, prompt)
	}
	retrievalConfig, err := json.Marshal(struct {
		Config    knowledge.Config
		Overrides *knowledge.RetrievalSettings
	}{agentDef.KBaseConfig, agentDef.KBaseConfig.RetrievalOverrides})
	if err != nil {
		return contracts.QuerySession{}, err
	}
	kbaseModePrompts := contracts.KBaseModePrompts{}
	if agentbuiltin.IsKBaseMode(agentDef.Mode) {
		kbaseModePrompts = contracts.KBaseModePrompts{
			Workspace: s.deps.Config.KBasePrompts.WorkspacePrompt,
		}
	}
	resolvedPlanningSettings := contracts.ResolvePlanningModeSettings(agentDef.StageSettings, s.deps.Config.Defaults.CoderPlanning.MaxSteps)

	planningMode := s.deps.Config.PlanningMode.Effective()
	session := contracts.QuerySession{
		RequestID:                 req.RequestID,
		RunID:                     req.RunID,
		TempRoot:                  SystemTempRoot(),
		TempRoots:                 SystemTempRoots(),
		SubTaskID:                 options.SubTaskID,
		ChatID:                    req.ChatID,
		ChatName:                  summary.ChatName,
		AgentKey:                  req.AgentKey,
		RunOwner:                  contracts.AgentRunOwner(req.AgentKey),
		AgentName:                 agentDef.Name,
		AgentRole:                 agentDef.Role,
		AgentDescription:          agentDef.Description,
		Locale:                    s.deps.Config.Prompts.Runtime.ResolveLocale(options.Locale),
		EnvironmentPromptTemplate: s.deps.Config.Prompts.Runtime.Template(),
		ModelKey:                  agentDef.ModelKey,
		MaxInputTokens:            agentDef.MaxInputTokens,
		ToolNames:                 toolNames,
		ToolSetFrozen:             true,
		ProtectedPaths:            accesspolicy.PlatformProtectedPaths(s.deps.Config),
		ControlReadonlyRoots:      accesspolicy.PlatformControlReadonlyRoots(s.deps.Config),
		PathAppend:                ResolveSkillPathAppend(agentDef, agentDef.EffectiveSkills(), s.deps.Config.Bash.PathAppendRoots),
		Mode:                      agentDef.Mode,
		ModeCapabilities:          ResolvedModeCapabilities(agentDef),
		SupportsContextCompaction: !IsProxyRoutedAgent(agentDef),
		KBaseBindingLibraryID:     agentDef.KBaseConfig.LibraryID,
		KBaseRetrievalConfig:      string(retrievalConfig),
		CapabilityPrompts:         capabilityPrompts,
		PlanningMode:              planningModeRequested,
		PlanningExcludeTools:      append([]string(nil), planningMode.ExcludeTools...),
		ExecuteExcludeTools:       append([]string(nil), planningMode.ExecuteExcludeTools...),
		EditingMode:               editingMode,
		WorkspaceReadOnly:         !editingMode,
		Detail:                    stream.ResolveDetailLevel(req.Detail, agentDef.Interaction().DefaultDetail),

		Created:              options.Created,
		ConnectorDirs:        RuntimeConnectorDirs(agentDef),
		MCPServerKeys:        mcpServerKeys,
		SkillDirs:            RuntimeSkillDirs(agentDef),
		SharedSkillsRoot:     s.deps.Config.Paths.EffectiveRUSkillsDir(),
		SharedConnectorsRoot: s.deps.Config.Paths.ConnectorSources().SharedRoot(),
		NativeConnectorTools: RuntimeNativeConnectorTools(agentDef),
		ConnectorCLIEntries:  append([]connector.CLIEntry(nil), agentDef.ConnectorCLIEntries...),
		SkillIDs:             append([]string(nil), agentDef.EffectiveSkills()...),

		ConnectorBinDirs:         append([]string(nil), agentDef.ConnectorBinDirs...),
		ConnectorInstallBinDirs:  agentDef.ConnectorInstallBinDirs(),
		ConnectorEnv:             agentconfig.Merge(agentDef.ConnectorEnv),
		ConnectorCredentials:     agentDef.ConnectorCredentials,
		ContextTags:              append([]string(nil), agentDef.ContextTags...),
		Budget:                   contracts.CloneMap(agentDef.Budget),
		StageSettings:            contracts.CloneMap(agentDef.StageSettings),
		ResolvedBudget:           contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedPlanningSettings: resolvedPlanningSettings,
		HistoryMessages:          historyMessages,
		RuntimeContext:           runtimeContext,
		PromptAppend:             promptAppend,
		AdvancedUserPrompt:       s.deps.Config.Query.AdvancedUserPrompt && !IsProxyRoutedAgent(agentDef),
		SkillCatalogPrompt:       skillCatalogPrompt,
		SoulPrompt:               agentDef.SoulPrompt,
		AgentsPrompt:             agentDef.AgentsPrompt,
		WorkspaceAgentsPrompt:    workspaceAgentsPrompt,
		ModeSystemPrompt:         agentbuiltin.ConfiguredSystemPrompt(agentDef.Mode, s.deps.Config.CoderPrompts.SystemPrompt, s.deps.Config.KBasePrompts.SystemPrompt),
		KBaseModePrompts:         kbaseModePrompts,
		RuntimeEnvironmentID:     ExtractRuntimeField(agentDef.Runtime, "environmentId"),
		RuntimeLevel:             ExtractRuntimeField(agentDef.Runtime, "level"),
		RuntimeExtraMounts:       RuntimeConnectorMounts(RuntimeExtraMountsForSkillReferences(agentDef.Runtime["sandboxMounts"], agentDef.Interaction().Attachment.Skills && HasRuntimeSandbox(agentDef.Runtime)), agentDef),
		RuntimeHostAccess:        RuntimeHostAccess(agentDef.HostAccess),
		RunAccessRoots:           runAccessRoots,
		AgentHasRuntimeSandbox:   HasRuntimeSandbox(agentDef.Runtime),
		WorkspaceRoot:            resolvedWorkspaceRoot,
		ChatRoot:                 strings.TrimSpace(runtimeContext.LocalPaths.ChatDir),
		AccessLevel:              ResolveQueryAccessLevel(req, agentDef),
		InteractionConfig:        func() *interaction.Config { c := agentDef.Interaction(); return &c }(),
		StaticRuntimeEnv:         runtimeEnvOverrides,
	}
	if agentbuiltin.NativePlanning(agentDef.Mode, agentDef.ACPBridgeID) && !session.PlanningMode && agentbuiltin.IsConfirmedPlanRun(req.Params) {
		// A Run started from a confirmed plan is an ordinary Run of this Agent
		// with the configured execution exclusions applied to its tools.
		session.ConfirmedPlanRun = true
		if s.deps.Tools != nil {
			session.ToolNames = agentbuiltin.ConfirmedPlanTools(session, s.deps.Tools.Definitions())
		} else {
			session.ToolNames = agentbuiltin.ConfirmedPlanTools(session, nil)
		}
	}
	if !options.DisableSkillScriptGrants && !IsProxyRoutedAgent(agentDef) && !agentbuiltin.IsCoderACPBackend(agentDef.Mode, agentDef.ACPBridgeID) {
		session.SkillScripts = BuildSkillScriptScope(session, agentDef, nil)
	}
	if options.SubTaskID == "" && !IsProxyRoutedAgent(agentDef) && ContainsTool(agentDef.Tools, "run_env") {
		if existing, ok := LookupRunEnvironment(s.deps.Runs, req.RunID); ok {
			session.RunEnvironment = existing
		} else {
			session.RunEnvironment = s.NewRunEnvironmentScope()
		}
	}
	if ShouldLoadPlanTaskContext(session) {
		session.PlanTaskContext = s.loadPlanTaskContext(req.ChatID)
	}
	if session.AgentHasRuntimeSandbox && !s.deps.Config.ContainerHub.Enabled {
		return contracts.QuerySession{}, fmt.Errorf("agent %q requires sandbox but container-hub is disabled", req.AgentKey)
	}
	if principal != nil {
		session.Subject = principal.Subject
	}

	personalMemory := memory.NewStore(s.deps.Config.Paths.MemoryDir, s.deps.Config.Paths.OwnerDir, nil)
	if ContainsTool(agentDef.ContextTags, "owner") {
		owner, err := personalMemory.Read("owner", "")
		if err != nil {
			return contracts.QuerySession{}, fmt.Errorf("load OWNER.md: %w", err)
		}
		session.OwnerPrompt, session.OwnerPromptLoaded = owner.Content, true
	}
	if options.IncludeMemory && s.deps.Config.Memory.Enabled && !IsProxyRoutedAgent(agentDef) && catalog.AgentEngineForAPI(agentDef) == catalog.AgentEngineNative {
		for _, layer := range []struct {
			tag, key string
			budget   config.MemorySummaryBudget
			target   *string
		}{
			{"memory-global", "", s.deps.Config.Memory.Summary.Global, &session.GlobalMemoryContext},
			{"memory-agent", agentDef.Key, s.deps.Config.Memory.Summary.Agent, &session.AgentMemoryContext},
		} {
			if !ContainsTool(agentDef.ContextTags, layer.tag) {
				continue
			}
			doc, err := personalMemory.ReadSummary(layer.key)
			if err != nil {
				return contracts.QuerySession{}, err
			}
			if !doc.Exists {
				continue
			}
			if !memory.SummaryWithinBudget(doc.Content, layer.key != "", layer.budget) {
				log.Printf("memory summary omitted: agent=%q tag=%s exceeds summary budget", agentDef.Key, layer.tag)
				continue
			}
			*layer.target = memory.SummaryPrompt(doc, layer.key != "", session.Locale)
		}
	}
	session.ReferencesScope = contracts.NewRunReferences(session.SkillScripts)
	if options.DisableSkillScriptGrants {
		// Same-Run recovery revalidates each recorded pointer independently.
		// Invalid resources remain visible, but receive no restored grant.
		for _, ref := range req.References {
			grant, err := s.PrepareRunReferences(ctx, session, agentDef, []queryinput.Reference{ref}, false, false, options.SkipInteractionPolicy)
			if err == nil {
				_ = session.ReferencesScope.Commit(grant)
			}
		}
	} else {
		grant, err := s.PrepareRunReferences(ctx, session, agentDef, req.References, false, true, options.SkipInteractionPolicy)
		if err != nil {
			return contracts.QuerySession{}, err
		}
		if err = session.ReferencesScope.Commit(grant); err != nil {
			return contracts.QuerySession{}, err
		}
		req.References = grant.References
	}
	session.RuntimeContext.References = req.References
	bScripts := !options.DisableSkillScriptGrants
	s.BindReferencePreparer(&session, agentDef, bScripts)
	session.CurrentMessages = s.BuildCurrentMessages(req, session)
	if err := s.ConfigureViews(&session, agentDef); err != nil {
		return contracts.QuerySession{}, err
	}
	return session, nil
}

type RunEnvironmentLookup interface {
	RunEnvironment(runID string) (*runenv.Scope, bool)
}

func LookupRunEnvironment(runs contracts.RunManager, runID string) (*runenv.Scope, bool) {
	lookup, ok := runs.(RunEnvironmentLookup)
	if !ok || lookup == nil {
		return nil, false
	}
	return lookup.RunEnvironment(strings.TrimSpace(runID))
}

func (s *Builder) NewRunEnvironmentScope() *runenv.Scope {
	cfg := s.deps.Config.RunEnv
	return runenv.NewScope(runenv.Limits{
		MaxDynamicKeys:  cfg.MaxDynamicKeys,
		MaxValueBytes:   cfg.MaxValueBytes,
		MaxTotalBytes:   cfg.MaxTotalBytes,
		ExtraDeniedKeys: append([]string(nil), cfg.DenyKeys...),
	})
}

func ContainsTool(tools []string, wanted string) bool {
	for _, tool := range tools {
		if strings.EqualFold(strings.TrimSpace(tool), wanted) {
			return true
		}
	}
	return false
}

func SystemTempRoot() string {
	root, ok := temppaths.System().Primary()
	if !ok {
		return ""
	}
	return root.Host
}

func SystemTempRoots() []string {
	return temppaths.System().Paths()
}

func ExcludeHistoryRun(messages []map[string]any, runID string) []map[string]any {
	runID = strings.TrimSpace(runID)
	if runID == "" || len(messages) == 0 {
		return messages
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(contracts.AnyStringNode(message["runId"])) == runID {
			continue
		}
		out = append(out, message)
	}
	return out
}

func (s *Builder) BuildCurrentMessages(req runtimetypes.QueryCommand, session contracts.QuerySession) []map[string]any {
	isVision := false
	if s != nil && s.deps.Models != nil {
		if model, err := s.deps.Models.GetModel(session.ModelKey); err == nil {
			isVision = model.IsVision
		}
	}
	return querymessages.BuildMessagesWithOptions(s.deps.Config.Paths.ChatsDir, req.ChatID, req.Role, req.Message, req.References, isVision, false, querymessages.BuildOptions{
		AdvancedUserPrompt: session.AdvancedUserPrompt,
		WorkspaceDir:       session.WorkspaceRoot,
		ChatDir:            session.ChatRoot,
		RunID:              session.RunID,
		RequestID:          session.RequestID,
		AgentKey:           session.AgentKey,

		AutomationID:                 querymessages.AutomationIDFromChatSource(req.ChatSource),
		AutomationUserPromptTemplate: s.deps.Config.Prompts.Automation.UserPromptTemplate,

		Scene:     req.Scene,
		TaskInput: session.ResultContract.TaskInput(),
	})
}

func ShouldLoadPlanTaskContext(session contracts.QuerySession) bool {
	if session.PlanningMode {
		return false
	}
	for _, name := range session.ToolNames {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case contracts.PlanGetTasksToolName, contracts.PlanUpdateTaskToolName:
			return true
		}
	}
	return false
}

func (s *Builder) loadPlanTaskContext(chatID string) string {
	if s == nil {
		return ""
	}
	state, err := plantasks.LoadLatestStateForChat(s.deps.Config.Paths.ChatsDir, chatID)
	if err != nil {
		log.Printf("[server][plan] load plan task context failed chatId=%s err=%v", chatID, err)
		return ""
	}
	return plantasks.FormatStateContext(state)
}

func ResolvedModeCapabilities(def catalog.AgentDefinition) agentcontract.ModeCapabilities {
	return catalog.ResolvedModeCapabilities(def)
}

func ProjectPromptFiles(files []catalog.AgentProjectPromptFile) []agentbuiltin.ProjectPromptFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]agentbuiltin.ProjectPromptFile, 0, len(files))
	for _, file := range files {
		out = append(out, agentbuiltin.ProjectPromptFile{Source: file.Source, Path: file.Path})
	}
	return out
}

func NormalizedAccessLevel(value string) string {
	normalized, ok := contracts.NormalizeAccessLevel(value)
	if !ok {
		return contracts.AccessLevelDefault
	}
	return normalized
}

func RuntimeHostAccess(cfg catalog.AgentHostAccessConfig) contracts.HostAccessRoots {
	return contracts.HostAccessRoots{
		ReadRoots:  append([]string(nil), cfg.ReadRoots...),
		WriteRoots: append([]string(nil), cfg.WriteRoots...),
	}
}

func BuildSessionToolNames(base []string, allowInvokeAgents bool) []string {
	tools := make([]string, 0, len(base))
	seen := map[string]struct{}{}
	for _, tool := range base {
		name := strings.TrimSpace(tool)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		if !allowInvokeAgents && key == strings.ToLower(contracts.InvokeAgentsToolName) {
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, name)
	}
	return tools
}

// RuntimeModeToolNames hides native connector tools the current runtime cannot
// execute, so the model is not offered page control without a Desktop host.
func RuntimeModeToolNames(tools []string, mode config.RuntimeMode) []string {
	if mode == config.RuntimeModeDesktop {
		return tools
	}
	kept := make([]string, 0, len(tools))
	for _, tool := range tools {
		if !connector.NativeToolRequiresDesktop(tool) {
			kept = append(kept, tool)
		}
	}
	return kept
}

// SessionMCPTools adds the Agent's mounted MCP tools. In MCP tool mode they are
// replaced by mcp_search and mcp_call, offered only when an MCP server is
// mounted and the Agent does not exclude them; the returned server keys bound
// what the two tools may reach. Outside tool mode the two tools never appear.
func SessionMCPTools(agentTools []string, agentDef catalog.AgentDefinition, toolMode bool, mountedTools func() []string) ([]string, []string) {
	tools := make([]string, 0, len(agentTools)+len(contracts.MCPGatewayToolNames))
	for _, name := range agentTools {
		if !contracts.IsMCPGatewayToolName(name) {
			tools = append(tools, name)
		}
	}
	if !toolMode {
		return append(tools, mountedTools()...), nil
	}
	if len(agentDef.ConnectorMCPServers) == 0 {
		return tools, nil
	}
	for _, name := range contracts.MCPGatewayToolNames {
		if !ContainsTool(agentDef.ExcludedTools, name) {
			tools = append(tools, name)
		}
	}
	return tools, append([]string(nil), agentDef.ConnectorMCPServers...)
}

type McpServerToolResolver interface {
	MCPToolNamesForServers(serverKeys []string) []string
}

func McpToolNamesForServers(tools contracts.ToolExecutor, serverKeys []string) []string {
	resolver, ok := tools.(McpServerToolResolver)
	if !ok || resolver == nil || len(serverKeys) == 0 {
		return nil
	}
	return resolver.MCPToolNamesForServers(serverKeys)
}
