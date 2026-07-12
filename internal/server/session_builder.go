package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"agent-platform/internal/plantasks"
	"agent-platform/internal/querymessages"
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
		agentKey:   req.AgentKey,
		teamID:     req.TeamID,
		role:       defaultRole(req.Role),
		chatID:     req.ChatID,
		chatName:   summary.ChatName,
		scene:      req.Scene,
		references: req.References,
		principal:  principal,
		definition: agentDef,
	})
	if err != nil {
		return contracts.QuerySession{}, err
	}
	req.References = runtimeContext.References

	promptAppend := buildPromptAppendConfig(s.deps.Config.Prompts, agentDef)
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
		AgentDir:                agentDef.AgentDir,
		WorkspaceRoot:           resolvedWorkspaceRoot,
		ProjectPromptFiles:      coderProjectPromptFiles(agentDef.Project.PromptFiles),
		WorkspaceAgentsEnabled:  s.deps.Config.CoderSettings.WorkspaceAgents.Enabled,
		WorkspaceAgentsFileName: s.deps.Config.CoderSettings.WorkspaceAgents.File,
	})
	if err != nil {
		return contracts.QuerySession{}, err
	}
	skillHookDirs, runtimeEnvOverrides := resolveSkillRuntimeSettings(
		runtimeAgentEnv(agentDef.Runtime["env"]),
		agentDef.AgentDir,
		s.deps.Config.Paths.SkillsMarketDir,
		agentDef.Skills,
	)
	log.Printf("[server][skill-runtime] agent=%s skills=%v hookDirs=%v runtimeEnvKeys=%v",
		agentDef.Key,
		agentDef.Skills,
		skillHookDirs,
		sortedStringKeys(runtimeEnvOverrides),
	)

	configuredToolNames := effectiveAgentTools(agentDef)
	toolNames := buildSessionToolNames(configuredToolNames, options.AllowInvokeAgents)
	if !options.AllowInvokeAgents {
		switch {
		case len(configuredToolNames) == 0 && s.deps.Tools != nil:
			// An empty allowlist normally means "all default tools" to the LLM
			// layer. Materialize that default set here so agent_invoke can be
			// removed from child sessions instead of being reintroduced later.
			toolNames = defaultSessionToolNamesWithoutInvoke(s.deps.Tools.Definitions())
		case len(configuredToolNames) > 0 && len(toolNames) == 0:
			// Preserve an explicit "no remaining tools" selection. A non-matching
			// sentinel makes the downstream definition filter return an empty set.
			toolNames = []string{"__no_child_tools__"}
		}
	}
	toolNames = agentcoder.RuntimeToolNamesForAgent(agentDef.Mode, agentDef.ACPBridgeID, agentcoder.MainStage, toolNames)

	session := contracts.QuerySession{
		RequestID:                     req.RequestID,
		RunID:                         req.RunID,
		SubTaskID:                     options.SubTaskID,
		ChatID:                        req.ChatID,
		ChatName:                      summary.ChatName,
		AgentKey:                      req.AgentKey,
		AgentName:                     agentDef.Name,
		AgentRole:                     agentDef.Role,
		AgentDescription:              agentDef.Description,
		Locale:                        options.Locale,
		ModelKey:                      agentDef.ModelKey,
		ToolNames:                     toolNames,
		Mode:                          agentDef.Mode,
		ModeCapabilities:              resolvedModeCapabilities(agentDef),
		PlanningMode:                  agentcoder.PlanningModeEnabled(agentDef.Mode, req.PlanningMode != nil && *req.PlanningMode),
		TeamID:                        req.TeamID,
		Created:                       options.Created,
		SkillKeys:                     append([]string(nil), agentDef.Skills...),
		ContextTags:                   append([]string(nil), agentDef.ContextTags...),
		Budget:                        contracts.CloneMap(agentDef.Budget),
		StageSettings:                 contracts.CloneMap(agentDef.StageSettings),
		ResolvedBudget:                contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedPlanExecuteSettings:   contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask),
		ResolvedCoderPlanningSettings: contracts.ResolveCoderPlanningSettings(agentDef.StageSettings, s.deps.Config.Defaults.CoderPlanning.MaxSteps),
		HistoryMessages:               historyMessages,
		StableMemoryContext:           stableMemoryContext,
		SessionMemoryContext:          sessionMemoryContext,
		ObservationContext:            observationContext,
		MemoryUsageSummary:            memoryUsageSummary,
		RuntimeContext:                runtimeContext,
		PromptAppend:                  promptAppend,
		AdvancedUserPrompt:            s.deps.Config.Query.AdvancedUserPrompt && !isProxyRoutedAgent(agentDef),
		StaticMemoryPrompt:            staticMemoryPrompt,
		SkillCatalogPrompt:            buildSkillCatalogPrompt(agentDef, s.deps.Config.Paths.SkillsMarketDir, promptAppend),
		SoulPrompt:                    agentDef.SoulPrompt,
		AgentsPrompt:                  agentDef.AgentsPrompt,
		WorkspaceAgentsPrompt:         workspaceAgentsPrompt,
		PlanPrompt:                    agentDef.PlanPrompt,
		ExecutePrompt:                 agentDef.ExecutePrompt,
		SummaryPrompt:                 agentDef.SummaryPrompt,
		ModeSystemPrompt:              agentbuiltin.ConfiguredSystemPrompt(agentDef.Mode, s.deps.Config.CoderPrompts.SystemPrompt, s.deps.Config.KBasePrompts.SystemPrompt),
		RuntimeEnvironmentID:          extractRuntimeField(agentDef.Runtime, "environmentId"),
		RuntimeLevel:                  extractRuntimeField(agentDef.Runtime, "level"),
		RuntimeExtraMounts:            runtimeExtraMounts(agentDef.Runtime["sandboxMounts"]),
		RuntimeHostAccess:             runtimeHostAccess(agentDef.HostAccess),
		AgentHasRuntimeSandbox:        hasRuntimeSandbox(agentDef.Runtime),
		AgentHasMemoryConfig:          agentDef.MemoryEnabled,
		WorkspaceRoot:                 resolvedWorkspaceRoot,
		AccessLevel:                   normalizedAccessLevel(req.AccessLevel),
		SkillHookDirs:                 skillHookDirs,
		RuntimeEnvOverrides:           runtimeEnvOverrides,
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

func defaultSessionToolNamesWithoutInvoke(defs []api.ToolDetailResponse) []string {
	tools := make([]string, 0, len(defs))
	seen := map[string]struct{}{}
	for _, def := range defs {
		if explicitOnly, _ := def.Meta["explicitOnly"].(bool); explicitOnly {
			continue
		}
		name := strings.TrimSpace(def.Name)
		if name == "" {
			name = strings.TrimSpace(def.Key)
		}
		key := strings.ToLower(name)
		if key == "" || key == strings.ToLower(contracts.InvokeAgentsToolName) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, name)
	}
	return tools
}
