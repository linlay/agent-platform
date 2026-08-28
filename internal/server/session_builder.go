package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	agentcoder "agent-platform/internal/agent/coder"
	agentkbase "agent-platform/internal/agent/kbase"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/kbase"
	"agent-platform/internal/memory"
	"agent-platform/internal/plantasks"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/runenv"
	"agent-platform/internal/temppaths"
)

type querySessionBuildOptions struct {
	Created                bool
	SubTaskID              string
	Locale                 string
	IncludeHistory         bool
	IncludeMemory          bool
	AllowInvokeAgents      bool
	Principal              *Principal
	TeamHistoryAgentKey    string
	TeamCoordinatorHistory bool
}

var memoryInjectionEnabled = false

func (s *Server) BuildQuerySession(ctx context.Context, req api.QueryRequest, summary chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
	editingMode := agentkbase.EditingModeEnabled(agentDef.Mode, req.EditingMode != nil && *req.EditingMode)
	mustUseSkills, err := s.resolveQueryMustUseSkills(agentDef, req.MustUseSkills)
	if err != nil {
		return contracts.QuerySession{}, mustUseSkillUnavailableStatus(err)
	}
	runAccessRoots, err := mustUseSkillRunAccess(mustUseSkills.Skills)
	if err != nil {
		return contracts.QuerySession{}, mustUseSkillUnavailableStatus(err)
	}
	req.MustUseSkills = mustUseSkills.Keys
	if !strings.EqualFold(strings.TrimSpace(agentDef.Mode), agentteam.Mode) {
		if err := catalog.ValidateOrdinaryAgentTools(agentDef.Tools); err != nil {
			return contracts.QuerySession{}, err
		}
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
				historyMessages = excludeHistoryRun(historyMessages, req.RunID)
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

	var staticMemoryPrompt string
	var stableMemoryContext string
	var sessionMemoryContext string
	var observationContext string
	var memoryUsageSummary *api.MemoryUsageSummary
	if memoryInjectionEnabled {
		staticMemoryPrompt = strings.TrimSpace(agentDef.StaticMemoryPrompt)
	}
	if memoryInjectionEnabled && options.IncludeMemory && s.memoryEnabledForAgent(agentDef) && s.deps.Memory != nil && req.Message != "" {
		topN := s.deps.Config.Memory.ContextTopN
		if topN <= 0 {
			topN = 5
		}
		maxChars := s.deps.Config.Memory.ContextMaxChars
		if maxChars <= 0 {
			maxChars = 4000
		}
		userKey := ""
		principal := options.Principal
		if principal == nil {
			principal = PrincipalFromContext(ctx)
		}
		if principal != nil {
			userKey = strings.TrimSpace(principal.Subject)
		}
		if bundle, err := s.deps.Memory.BuildContextBundle(memory.ContextRequest{
			AgentKey:     req.AgentKey,
			TeamID:       req.TeamID,
			ChatID:       req.ChatID,
			UserKey:      userKey,
			Query:        req.Message,
			TopFacts:     topN,
			TopObs:       topN,
			MaxChars:     maxChars,
			FreezeStable: true,
		}); err != nil {
			log.Printf("[memory][context] build context bundle failed (chatId=%s agentKey=%s): %v", req.ChatID, req.AgentKey, err)
		} else {
			stableMemoryContext = strings.TrimSpace(bundle.StablePrompt)
			sessionMemoryContext = strings.TrimSpace(bundle.SessionPrompt)
			observationContext = strings.TrimSpace(bundle.ObservationPrompt)
			memoryUsageSummary = buildMemoryUsageSummary(staticMemoryPrompt, bundle)
		}
	}

	principal := options.Principal
	if principal == nil {
		principal = PrincipalFromContext(ctx)
	}
	runtimeContext, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey:           req.AgentKey,
		teamID:             req.TeamID,
		role:               defaultRole(req.Role),
		chatID:             req.ChatID,
		chatName:           summary.ChatName,
		scene:              req.Scene,
		references:         req.References,
		principal:          principal,
		definition:         agentDef,
		exposeSkillsCenter: mustUseSkills.HasExtraSkills,
	})
	if err != nil {
		return contracts.QuerySession{}, err
	}
	req.References = runtimeContext.References

	promptAppend := buildPromptAppendConfig(s.deps.Config.Prompts, agentDef)
	skillCatalogPrompt := buildSkillCatalogPrompt(agentDef, s.deps.Config.Paths.SkillsCenterDir, promptAppend, mustUseSkills.Skills...)
	if mustUseSkillConstraint := buildMustUseSkillConstraint(mustUseSkills.Skills); mustUseSkillConstraint != "" {
		if skillCatalogPrompt != "" {
			skillCatalogPrompt += "\n\n" + mustUseSkillConstraint
		} else {
			skillCatalogPrompt = mustUseSkillConstraint
		}
	}
	resolvedWorkspaceRoot := strings.TrimSpace(runtimeContext.LocalPaths.WorkspaceDir)
	if err := agentcoder.ValidateWorkspaceGit(agentcoder.WorkspaceGitPolicy{
		Mode:           agentDef.Mode,
		WorkspaceRoot:  resolvedWorkspaceRoot,
		ExpectedBranch: agentDef.Project.Git.ExpectedBranch,
	}); err != nil {
		return contracts.QuerySession{}, err
	}
	workspaceAgentsPrompt, err := agentcoder.LoadWorkspacePrompt(agentcoder.WorkspacePromptPolicy{
		Mode:                    agentDef.Mode,
		ACPBridgeID:             agentDef.ACPBridgeID,
		AgentDir:                agentDef.RuntimeDir,
		WorkspaceRoot:           resolvedWorkspaceRoot,
		ProjectPromptFiles:      coderProjectPromptFiles(agentDef.Project.PromptFiles),
		WorkspaceAgentsEnabled:  s.deps.Config.CoderSettings.WorkspaceAgents.Enabled,
		WorkspaceAgentsFileName: s.deps.Config.CoderSettings.WorkspaceAgents.File,
	})
	if err != nil {
		return contracts.QuerySession{}, err
	}
	skillHookDirs, runtimeEnvOverrides, err := resolveSkillRuntimeSettings(
		runtimeAgentEnv(agentDef.Runtime["env"]),
		agentDef.RuntimeDir,
		s.deps.Config.Paths.SkillsCenterDir,
		agentDef.Skills,
	)
	if err != nil {
		return contracts.QuerySession{}, err
	}
	log.Printf("[server][skill-runtime] agent=%s skills=%v hookDirs=%v runtimeEnvKeys=%v",
		agentDef.Key,
		agentDef.Skills,
		skillHookDirs,
		sortedStringKeys(runtimeEnvOverrides),
	)

	configuredToolNames := withoutMCPToolNames(s.deps.Tools, effectiveAgentTools(agentDef))
	boundMCPToolNames := boundMCPToolNamesForAgent(s.deps.Tools, agentDef)
	configuredToolNames = append(configuredToolNames, boundMCPToolNames...)
	toolNames := buildSessionToolNames(configuredToolNames, options.AllowInvokeAgents)
	toolNames = agentcoder.RuntimeToolNamesForAgent(agentDef.Mode, agentDef.ACPBridgeID, agentcoder.MainStage, toolNames)
	if agentkbase.IsMode(agentDef.Mode) {
		toolNames = agentkbase.DefaultToolNames()
	}
	log.Printf("[server][session-tools] agent=%s mode=%s count=%d tools=%v", agentDef.Key, agentDef.Mode, len(toolNames), toolNames)
	capabilityPrompts := []string(nil)
	if agentDef.KBaseConfig.Enabled && !strings.EqualFold(agentDef.Mode, catalog.AgentModeKBase) {
		capabilityPrompts = append(capabilityPrompts, kbase.DefaultCapabilityPrompt)
	}
	resolvedPlanExecuteSettings := contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask)
	resolvedCoderPlanningSettings := contracts.ResolveCoderPlanningSettings(agentDef.StageSettings, s.deps.Config.Defaults.CoderPlanning.MaxSteps)
	if agentDef.KBaseConfig.Enabled {
		resolvedPlanExecuteSettings.Plan.Tools = appendKBaseCapabilityToolsToExplicitStage(resolvedPlanExecuteSettings.Plan.Tools)
		resolvedPlanExecuteSettings.Execute.Tools = appendKBaseCapabilityToolsToExplicitStage(resolvedPlanExecuteSettings.Execute.Tools)
		resolvedCoderPlanningSettings.Execute.Tools = appendKBaseCapabilityToolsToExplicitStage(resolvedCoderPlanningSettings.Execute.Tools)
	}
	var scopedFilePolicy *contracts.ScopedFilePolicy
	if agentkbase.IsMode(agentDef.Mode) {
		scopedFilePolicy = &contracts.ScopedFilePolicy{
			WorkspaceRoot:            resolvedWorkspaceRoot,
			WorkspaceMutationEnabled: editingMode,
			RequireExistingParent:    true,
		}
	}

	session := contracts.QuerySession{
		RequestID:                     req.RequestID,
		RunID:                         req.RunID,
		TempRoot:                      systemTempRoot(),
		TempRoots:                     systemTempRoots(),
		SubTaskID:                     options.SubTaskID,
		ChatID:                        req.ChatID,
		ChatName:                      summary.ChatName,
		AgentKey:                      req.AgentKey,
		RunOwner:                      contracts.AgentRunOwner(req.AgentKey, req.TeamID),
		AgentName:                     agentDef.Name,
		AgentRole:                     agentDef.Role,
		AgentDescription:              agentDef.Description,
		Locale:                        options.Locale,
		ModelKey:                      agentDef.ModelKey,
		ToolNames:                     toolNames,
		MCPToolNames:                  append([]string(nil), boundMCPToolNames...),
		MCPGeneration:                 mcpCatalogGeneration(s.deps.Tools, boundMCPToolNames),
		Mode:                          agentDef.Mode,
		ModeCapabilities:              resolvedModeCapabilities(agentDef),
		SupportsContextCompaction:     !isProxyRoutedAgent(agentDef),
		KBaseEnabled:                  agentDef.KBaseConfig.Enabled,
		CapabilityPrompts:             capabilityPrompts,
		PlanningMode:                  agentcoder.PlanningModeEnabled(agentDef.Mode, req.PlanningMode != nil && *req.PlanningMode),
		EditingMode:                   editingMode,
		ScopedFilePolicy:              scopedFilePolicy,
		TeamID:                        req.TeamID,
		Created:                       options.Created,
		SkillKeys:                     append([]string(nil), agentDef.Skills...),
		MustUseSkills:                 append([]string(nil), req.MustUseSkills...),
		ContextTags:                   append([]string(nil), agentDef.ContextTags...),
		Budget:                        contracts.CloneMap(agentDef.Budget),
		StageSettings:                 contracts.CloneMap(agentDef.StageSettings),
		ResolvedBudget:                contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedPlanExecuteSettings:   resolvedPlanExecuteSettings,
		ResolvedCoderPlanningSettings: resolvedCoderPlanningSettings,
		HistoryMessages:               historyMessages,
		StableMemoryContext:           stableMemoryContext,
		SessionMemoryContext:          sessionMemoryContext,
		ObservationContext:            observationContext,
		MemoryUsageSummary:            memoryUsageSummary,
		RuntimeContext:                runtimeContext,
		PromptAppend:                  promptAppend,
		AdvancedUserPrompt:            s.deps.Config.Query.AdvancedUserPrompt && !isProxyRoutedAgent(agentDef),
		StaticMemoryPrompt:            staticMemoryPrompt,
		SkillCatalogPrompt:            skillCatalogPrompt,
		SoulPrompt:                    agentDef.SoulPrompt,
		AgentsPrompt:                  agentDef.AgentsPrompt,
		WorkspaceAgentsPrompt:         workspaceAgentsPrompt,
		PlanPrompt:                    agentDef.PlanPrompt,
		ExecutePrompt:                 agentDef.ExecutePrompt,
		SummaryPrompt:                 agentDef.SummaryPrompt,
		ModeSystemPrompt:              agentbuiltin.ConfiguredSystemPrompt(agentDef.Mode, s.deps.Config.CoderPrompts.SystemPrompt, s.deps.Config.KBasePrompts.SystemPrompt),
		RuntimeEnvironmentID:          extractRuntimeField(agentDef.Runtime, "environmentId"),
		RuntimeLevel:                  extractRuntimeField(agentDef.Runtime, "level"),
		RuntimeExtraMounts:            runtimeExtraMountsForMustUseSkills(agentDef.Runtime["sandboxMounts"], mustUseSkills.HasExtraSkills && hasRuntimeSandbox(agentDef.Runtime)),
		RuntimeHostAccess:             runtimeHostAccess(agentDef.HostAccess),
		RunAccessRoots:                runAccessRoots,
		AgentHasRuntimeSandbox:        hasRuntimeSandbox(agentDef.Runtime),
		AgentHasMemoryConfig:          agentDef.MemoryEnabled,
		WorkspaceRoot:                 resolvedWorkspaceRoot,
		ChatRoot:                      strings.TrimSpace(runtimeContext.LocalPaths.ChatDir),
		AccessLevel:                   normalizedAccessLevel(req.AccessLevel),
		SkillHookDirs:                 skillHookDirs,
		StaticRuntimeEnv:              runtimeEnvOverrides,
	}
	if options.SubTaskID == "" && strings.TrimSpace(req.TeamID) == "" && !isProxyRoutedAgent(agentDef) && containsTool(agentDef.Tools, "platform_control") {
		if existing, ok := lookupRunEnvironment(s.deps.Runs, req.RunID); ok {
			session.RunEnvironment = existing
		} else {
			session.RunEnvironment = s.newRunEnvironmentScope()
		}
	}
	if shouldLoadPlanTaskContext(session) {
		session.PlanTaskContext = s.loadPlanTaskContext(req.ChatID)
	}
	if session.AgentHasRuntimeSandbox && !s.deps.Config.ContainerHub.Enabled {
		return contracts.QuerySession{}, fmt.Errorf("agent %q requires sandbox but container-hub is disabled", req.AgentKey)
	}
	if principal != nil {
		session.Subject = principal.Subject
	}
	session.CurrentMessages = s.buildCurrentMessages(req, session)
	return session, nil
}

type runEnvironmentLookup interface {
	RunEnvironment(runID string) (*runenv.Scope, bool)
}

func lookupRunEnvironment(runs contracts.RunManager, runID string) (*runenv.Scope, bool) {
	lookup, ok := runs.(runEnvironmentLookup)
	if !ok || lookup == nil {
		return nil, false
	}
	return lookup.RunEnvironment(strings.TrimSpace(runID))
}

func (s *Server) newRunEnvironmentScope() *runenv.Scope {
	cfg := s.deps.Config.PlatformControl
	return runenv.NewScope(runenv.Limits{
		MaxDynamicKeys:  cfg.MaxDynamicKeys,
		MaxValueBytes:   cfg.MaxValueBytes,
		MaxTotalBytes:   cfg.MaxTotalBytes,
		ExtraDeniedKeys: append([]string(nil), cfg.DenyKeys...),
	})
}

func containsTool(tools []string, wanted string) bool {
	for _, tool := range tools {
		if strings.EqualFold(strings.TrimSpace(tool), wanted) {
			return true
		}
	}
	return false
}

func systemTempRoot() string {
	root, ok := temppaths.System().Primary()
	if !ok {
		return ""
	}
	return root.Host
}

func systemTempRoots() []string {
	return temppaths.System().Paths()
}

func appendKBaseCapabilityToolsToExplicitStage(tools []string) []string {
	if len(tools) == 0 {
		return nil
	}
	out := append([]string(nil), tools...)
	for _, toolName := range kbase.CapabilityToolNames() {
		if !containsString(out, toolName) {
			out = append(out, toolName)
		}
	}
	return out
}

func excludeHistoryRun(messages []map[string]any, runID string) []map[string]any {
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

func (s *Server) buildCurrentMessages(req api.QueryRequest, session contracts.QuerySession) []map[string]any {
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
		TeamID:             session.TeamID,
		Scene:              req.Scene,
	})
}

func shouldLoadPlanTaskContext(session contracts.QuerySession) bool {
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

func (s *Server) loadPlanTaskContext(chatID string) string {
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

func resolvedModeCapabilities(def catalog.AgentDefinition) agentcontract.ModeCapabilities {
	if descriptor, ok := agentbuiltin.Lookup(def.Mode); ok {
		capabilities := descriptor.Capabilities
		if agentcoder.IsACPBackend(def.Mode, def.ACPBridgeID) {
			capabilities.RunAsChild = false
		}
		return capabilities
	}
	switch strings.ToUpper(strings.TrimSpace(def.Mode)) {
	case "REACT", "ONESHOT", catalog.AgentModeProxy:
		return agentcontract.ModeCapabilities{InvokeChildren: true, RunAsChild: true}
	default:
		return agentcontract.ModeCapabilities{}
	}
}

func coderProjectPromptFiles(files []catalog.AgentProjectPromptFile) []agentcoder.ProjectPromptFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]agentcoder.ProjectPromptFile, 0, len(files))
	for _, file := range files {
		out = append(out, agentcoder.ProjectPromptFile{Source: file.Source, Path: file.Path})
	}
	return out
}

func normalizedAccessLevel(value string) string {
	normalized, ok := contracts.NormalizeAccessLevel(value)
	if !ok {
		return contracts.AccessLevelDefault
	}
	return normalized
}

func runtimeHostAccess(cfg catalog.AgentHostAccessConfig) contracts.HostAccessRoots {
	return contracts.HostAccessRoots{
		ReadRoots:  append([]string(nil), cfg.ReadRoots...),
		WriteRoots: append([]string(nil), cfg.WriteRoots...),
	}
}

func buildSessionToolNames(base []string, allowInvokeAgents bool) []string {
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

type mcpAgentToolBinder interface {
	MCPToolNamesForAgent(agentKey string) []string
}

type mcpToolCatalog interface {
	IsMCPTool(toolName string) bool
	MCPGeneration() int64
}

func boundMCPToolNames(tools contracts.ToolExecutor, agentKey string) []string {
	binder, ok := tools.(mcpAgentToolBinder)
	if !ok || binder == nil {
		return nil
	}
	return binder.MCPToolNamesForAgent(strings.TrimSpace(agentKey))
}

func boundMCPToolNamesForAgent(tools contracts.ToolExecutor, agentDef catalog.AgentDefinition) []string {
	mode := catalog.NormalizeAgentModeForRuntime(agentDef.Mode)
	if mode != "REACT" && mode != "PLAN_EXECUTE" {
		return nil
	}
	return boundMCPToolNames(tools, agentDef.Key)
}

func withoutMCPToolNames(tools contracts.ToolExecutor, names []string) []string {
	catalog, ok := tools.(mcpToolCatalog)
	if !ok || catalog == nil {
		return append([]string(nil), names...)
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		if !catalog.IsMCPTool(name) {
			result = append(result, name)
		}
	}
	return result
}

func mcpCatalogGeneration(tools contracts.ToolExecutor, boundNames []string) int64 {
	if len(boundNames) == 0 {
		return 0
	}
	catalog, ok := tools.(mcpToolCatalog)
	if !ok || catalog == nil {
		return 0
	}
	return catalog.MCPGeneration()
}
