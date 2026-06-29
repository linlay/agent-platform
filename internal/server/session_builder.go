package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"agent-platform/internal/querymessages"
)

type querySessionBuildOptions struct {
	Created           bool
	SubTaskID         string
	IncludeHistory    bool
	IncludeMemory     bool
	AllowInvokeAgents bool
	Principal         *Principal
}

var memoryInjectionEnabled = false

func (s *Server) BuildQuerySession(ctx context.Context, req api.QueryRequest, summary chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
	historyMessages := []map[string]any(nil)
	if options.IncludeHistory && s.deps.Chats != nil {
		historyMessages, _ = s.deps.Chats.LoadRawMessages(req.ChatID, chat.DefaultHistoryRunWindow)
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

	promptAppend := buildPromptAppendConfig(s.deps.Config.Prompts, agentDef)
	resolvedWorkspaceRoot := strings.TrimSpace(runtimeContext.LocalPaths.WorkspaceDir)
	if err := validateWorkspaceGitConfig(agentDef, resolvedWorkspaceRoot); err != nil {
		return contracts.QuerySession{}, err
	}
	workspaceAgentsPrompt, err := s.loadWorkspaceAgentsPrompt(agentDef, resolvedWorkspaceRoot)
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

	toolNames := buildSessionToolNames(effectiveAgentTools(agentDef), options.AllowInvokeAgents)
	if strings.EqualFold(agentDef.Mode, catalog.AgentModeCoder) && !catalog.AgentUsesACPCoderBackend(agentDef) {
		toolNames = contracts.AppendPlanTaskToolNames(toolNames)
	}

	session := contracts.QuerySession{
		RequestID:              req.RequestID,
		RunID:                  req.RunID,
		SubTaskID:              options.SubTaskID,
		ChatID:                 req.ChatID,
		ChatName:               summary.ChatName,
		AgentKey:               req.AgentKey,
		AgentName:              agentDef.Name,
		AgentRole:              agentDef.Role,
		AgentDescription:       agentDef.Description,
		ModelKey:               agentDef.ModelKey,
		ToolNames:              toolNames,
		Mode:                   agentDef.Mode,
		PlanningMode:           req.PlanningMode != nil && *req.PlanningMode && strings.EqualFold(agentDef.Mode, catalog.AgentModeCoder),
		TeamID:                 req.TeamID,
		Created:                options.Created,
		SkillKeys:              append([]string(nil), agentDef.Skills...),
		ContextTags:            append([]string(nil), agentDef.ContextTags...),
		Budget:                 contracts.CloneMap(agentDef.Budget),
		StageSettings:          contracts.CloneMap(agentDef.StageSettings),
		ResolvedBudget:         contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedStageSettings:  contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask),
		HistoryMessages:        historyMessages,
		StableMemoryContext:    stableMemoryContext,
		SessionMemoryContext:   sessionMemoryContext,
		ObservationContext:     observationContext,
		MemoryUsageSummary:     memoryUsageSummary,
		RuntimeContext:         runtimeContext,
		PromptAppend:           promptAppend,
		AdvancedUserPrompt:     s.deps.Config.Query.AdvancedUserPrompt && !isProxyRoutedAgent(agentDef),
		StaticMemoryPrompt:     staticMemoryPrompt,
		SkillCatalogPrompt:     buildSkillCatalogPrompt(agentDef, s.deps.Config.Paths.SkillsMarketDir, promptAppend),
		SoulPrompt:             agentDef.SoulPrompt,
		AgentsPrompt:           agentDef.AgentsPrompt,
		WorkspaceAgentsPrompt:  workspaceAgentsPrompt,
		PlanPrompt:             agentDef.PlanPrompt,
		ExecutePrompt:          agentDef.ExecutePrompt,
		SummaryPrompt:          agentDef.SummaryPrompt,
		CoderSystemPrompt:      coderSystemPrompt(agentDef.Mode, s.deps.Config.CoderPrompts.SystemPrompt),
		KBaseSystemPrompt:      kbaseSystemPrompt(agentDef.Mode, s.deps.Config.KBasePrompts.SystemPrompt),
		RuntimeEnvironmentID:   extractRuntimeField(agentDef.Runtime, "environmentId"),
		RuntimeLevel:           extractRuntimeField(agentDef.Runtime, "level"),
		RuntimeExtraMounts:     runtimeExtraMounts(agentDef.Runtime["sandboxMounts"]),
		RuntimeHostAccess:      runtimeHostAccess(agentDef.HostAccess),
		AgentHasRuntimeSandbox: hasRuntimeSandbox(agentDef.Runtime),
		AgentHasMemoryConfig:   agentDef.MemoryEnabled,
		WorkspaceRoot:          resolvedWorkspaceRoot,
		AccessLevel:            normalizedAccessLevel(req.AccessLevel),
		SkillHookDirs:          skillHookDirs,
		RuntimeEnvOverrides:    runtimeEnvOverrides,
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

func coderSystemPrompt(mode string, prompt string) string {
	if !strings.EqualFold(strings.TrimSpace(mode), catalog.AgentModeCoder) {
		return ""
	}
	return strings.TrimSpace(prompt)
}

func kbaseSystemPrompt(mode string, prompt string) string {
	if !strings.EqualFold(strings.TrimSpace(mode), catalog.AgentModeKBase) {
		return ""
	}
	return strings.TrimSpace(prompt)
}

func (s *Server) loadWorkspaceAgentsPrompt(agentDef catalog.AgentDefinition, workspaceRoot string) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(agentDef.Mode), catalog.AgentModeCoder) {
		return "", nil
	}
	if catalog.AgentUsesACPCoderBackend(agentDef) {
		return "", nil
	}
	if len(agentDef.Project.PromptFiles) > 0 {
		return loadConfiguredProjectPrompts(agentDef, workspaceRoot)
	}
	settings := s.deps.Config.CoderSettings.WorkspaceAgents
	if !settings.Enabled {
		return "", nil
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return "", nil
	}
	fileName := strings.TrimSpace(settings.File)
	if fileName == "" {
		return "", fmt.Errorf("coder workspace agents file is empty")
	}
	if filepath.IsAbs(fileName) {
		fileName = filepath.Base(fileName)
	}
	cleanFileName := filepath.Clean(fileName)
	if cleanFileName == "." || cleanFileName == ".." || strings.HasPrefix(cleanFileName, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid workspace AGENTS prompt path %q", fileName)
	}
	agentsPath := filepath.Join(workspaceRoot, cleanFileName)
	data, err := os.ReadFile(agentsPath)
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read workspace AGENTS prompt %s: %w", agentsPath, err)
}

func loadConfiguredProjectPrompts(agentDef catalog.AgentDefinition, workspaceRoot string) (string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	sections := make([]string, 0, len(agentDef.Project.PromptFiles))
	for _, promptFile := range agentDef.Project.PromptFiles {
		source, displayPath, fullPath, err := resolveProjectPromptPath(agentDef, workspaceRoot, promptFile)
		if err != nil {
			return "", err
		}
		if fullPath == "" {
			continue
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s project prompt %s: %w", source, fullPath, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		title := "Workspace " + displayPath
		if source == "agent" {
			title = "Agent " + displayPath
		}
		sections = append(sections, title+"\n"+content)
	}
	return strings.Join(sections, "\n\n"), nil
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

func resolveProjectPromptPath(agentDef catalog.AgentDefinition, workspaceRoot string, promptFile catalog.AgentProjectPromptFile) (string, string, string, error) {
	rawPath := strings.TrimSpace(promptFile.Path)
	if rawPath == "" {
		return "", "", "", nil
	}
	source := strings.ToLower(strings.TrimSpace(promptFile.Source))
	if source == "" {
		source = "workspace"
	}
	root := workspaceRoot
	displayPath := rawPath
	if source == "agent" {
		root = strings.TrimSpace(agentDef.AgentDir)
	} else if source != "workspace" {
		return "", "", "", fmt.Errorf("unsupported project prompt source %q for %q", promptFile.Source, rawPath)
	}
	if root == "" {
		return "", "", "", fmt.Errorf("%s project prompt root is empty for %q", source, rawPath)
	}
	if filepath.IsAbs(displayPath) {
		displayPath = filepath.Base(displayPath)
	}
	cleanPath := filepath.Clean(displayPath)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) {
		return "", "", "", fmt.Errorf("invalid %s project prompt path %q", source, rawPath)
	}
	return source, filepath.ToSlash(cleanPath), filepath.Join(root, cleanPath), nil
}

func validateWorkspaceGitConfig(agentDef catalog.AgentDefinition, workspaceRoot string) error {
	if !strings.EqualFold(strings.TrimSpace(agentDef.Mode), catalog.AgentModeCoder) {
		return nil
	}
	expectedBranch := strings.TrimSpace(agentDef.Project.Git.ExpectedBranch)
	if expectedBranch == "" {
		return nil
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required when projectConfig.git.expectedBranch is set")
	}
	currentBranch, err := readGitCurrentBranch(workspaceRoot)
	if err != nil {
		return fmt.Errorf("validate workspace git branch for %s: %w", workspaceRoot, err)
	}
	if currentBranch != expectedBranch {
		return fmt.Errorf("workspace git branch mismatch for %s: current %q, expected %q", workspaceRoot, currentBranch, expectedBranch)
	}
	return nil
}

func readGitCurrentBranch(workspaceRoot string) (string, error) {
	gitDirPath := filepath.Join(workspaceRoot, ".git")
	info, err := os.Stat(gitDirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("not a git repository")
		}
		return "", err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(gitDirPath)
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(data))
		const gitdirPrefix = "gitdir:"
		if !strings.HasPrefix(line, gitdirPrefix) {
			return "", fmt.Errorf("unsupported .git file")
		}
		target := strings.TrimSpace(strings.TrimPrefix(line, gitdirPrefix))
		if !filepath.IsAbs(target) {
			target = filepath.Join(workspaceRoot, target)
		}
		gitDirPath = filepath.Clean(target)
	}
	headPath := filepath.Join(gitDirPath, "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(data))
	const refPrefix = "ref: refs/heads/"
	if strings.HasPrefix(head, refPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(head, refPrefix)), nil
	}
	if head == "" {
		return "", fmt.Errorf("empty git HEAD")
	}
	return "", fmt.Errorf("detached HEAD %q", head)
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

func canUseInvokeAgentsTool(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "REACT", "ONESHOT", catalog.AgentModeCoder:
		return true
	default:
		return false
	}
}
