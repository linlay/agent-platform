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
)

type querySessionBuildOptions struct {
	Created           bool
	SubTaskID         string
	IncludeHistory    bool
	IncludeMemory     bool
	AllowInvokeAgents bool
	Principal         *Principal
}

func (s *Server) BuildQuerySession(ctx context.Context, req api.QueryRequest, summary chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
	historyMessages := []map[string]any(nil)
	if options.IncludeHistory && s.deps.Chats != nil {
		historyMessages, _ = s.deps.Chats.LoadRawMessages(req.ChatID, s.deps.Config.ChatStorage.K)
	}

	var stableMemoryContext string
	var sessionMemoryContext string
	var observationContext string
	var memoryUsageSummary *api.MemoryUsageSummary
	if options.IncludeMemory && s.memoryEnabledForAgent(agentDef) && s.deps.Memory != nil && req.Message != "" {
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
			memoryUsageSummary = buildMemoryUsageSummary(strings.TrimSpace(agentDef.StaticMemoryPrompt), bundle)
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
	if err := validateWorkspaceGitConfig(agentDef); err != nil {
		return contracts.QuerySession{}, err
	}
	workspaceAgentsPrompt, err := s.loadWorkspaceAgentsPrompt(agentDef)
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
		AgentType:              agentDef.Type,
		ModelKey:               agentDef.ModelKey,
		ToolNames:              buildSessionToolNames(effectiveAgentTools(agentDef), options.AllowInvokeAgents),
		Mode:                   agentDef.Mode,
		PlanningMode:           req.PlanningMode != nil && *req.PlanningMode && strings.EqualFold(agentDef.Mode, catalog.AgentModeCoder),
		ReactMaxSteps:          agentDef.ReactMaxSteps,
		TeamID:                 req.TeamID,
		Created:                options.Created,
		SkillKeys:              append([]string(nil), agentDef.Skills...),
		ContextTags:            append([]string(nil), agentDef.ContextTags...),
		Budget:                 contracts.CloneMap(agentDef.Budget),
		StageSettings:          contracts.CloneMap(agentDef.StageSettings),
		ToolOverrides:          s.buildSessionToolOverrides(agentDef),
		ResolvedBudget:         contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedStageSettings:  contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask),
		HistoryMessages:        historyMessages,
		StableMemoryContext:    stableMemoryContext,
		SessionMemoryContext:   sessionMemoryContext,
		ObservationContext:     observationContext,
		MemoryUsageSummary:     memoryUsageSummary,
		RuntimeContext:         runtimeContext,
		PromptAppend:           promptAppend,
		StaticMemoryPrompt:     strings.TrimSpace(agentDef.StaticMemoryPrompt),
		SkillCatalogPrompt:     buildSkillCatalogPrompt(agentDef, s.deps.Config.Paths.SkillsMarketDir, promptAppend),
		SoulPrompt:             agentDef.SoulPrompt,
		AgentsPrompt:           agentDef.AgentsPrompt,
		WorkspaceAgentsPrompt:  workspaceAgentsPrompt,
		PlanPrompt:             agentDef.PlanPrompt,
		ExecutePrompt:          agentDef.ExecutePrompt,
		SummaryPrompt:          agentDef.SummaryPrompt,
		RuntimeEnvironmentID:   extractRuntimeField(agentDef.Runtime, "environmentId"),
		RuntimeLevel:           extractRuntimeField(agentDef.Runtime, "level"),
		RuntimeExtraMounts:     runtimeExtraMounts(agentDef.Runtime["extraMounts"]),
		AgentHasRuntimeSandbox: hasRuntimeSandbox(agentDef.Runtime),
		AgentHasMemoryConfig:   agentDef.MemoryEnabled,
		WorkspaceRoot:          strings.TrimSpace(agentDef.Workspace.Root),
		SkillHookDirs:          skillHookDirs,
		RuntimeEnvOverrides:    runtimeEnvOverrides,
	}
	if session.AgentHasRuntimeSandbox && !s.deps.Config.ContainerHub.Enabled {
		return contracts.QuerySession{}, fmt.Errorf("agent %q requires sandbox but container-hub is disabled", req.AgentKey)
	}
	if principal != nil {
		session.Subject = principal.Subject
	}
	return session, nil
}

func (s *Server) loadWorkspaceAgentsPrompt(agentDef catalog.AgentDefinition) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(agentDef.Mode), catalog.AgentModeCoder) {
		return "", nil
	}
	if len(agentDef.Workspace.ProjectPromptFiles) > 0 {
		return loadConfiguredProjectPrompts(agentDef)
	}
	settings := s.deps.Config.CoderSettings.WorkspaceAgents
	if !settings.Enabled {
		return "", nil
	}
	workspaceRoot := strings.TrimSpace(agentDef.Workspace.Root)
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

func loadConfiguredProjectPrompts(agentDef catalog.AgentDefinition) (string, error) {
	workspaceRoot := strings.TrimSpace(agentDef.Workspace.Root)
	sections := make([]string, 0, len(agentDef.Workspace.ProjectPromptFiles))
	for _, promptFile := range agentDef.Workspace.ProjectPromptFiles {
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
		if source == "agent-managed" {
			title = "Agent-managed Project " + displayPath
		}
		sections = append(sections, title+"\n"+content)
	}
	return strings.Join(sections, "\n\n"), nil
}

func resolveProjectPromptPath(agentDef catalog.AgentDefinition, workspaceRoot string, promptFile string) (string, string, string, error) {
	raw := strings.TrimSpace(promptFile)
	if raw == "" {
		return "", "", "", nil
	}
	const agentPrefix = "agent:"
	source := "workspace"
	root := workspaceRoot
	displayPath := raw
	if strings.HasPrefix(raw, agentPrefix) {
		source = "agent-managed"
		root = strings.TrimSpace(agentDef.AgentDir)
		displayPath = strings.TrimSpace(strings.TrimPrefix(raw, agentPrefix))
	}
	if root == "" {
		return "", "", "", fmt.Errorf("%s project prompt root is empty for %q", source, raw)
	}
	if filepath.IsAbs(displayPath) {
		displayPath = filepath.Base(displayPath)
	}
	cleanPath := filepath.Clean(displayPath)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) {
		return "", "", "", fmt.Errorf("invalid %s project prompt path %q", source, raw)
	}
	return source, filepath.ToSlash(cleanPath), filepath.Join(root, cleanPath), nil
}

func validateWorkspaceGitConfig(agentDef catalog.AgentDefinition) error {
	if !strings.EqualFold(strings.TrimSpace(agentDef.Mode), catalog.AgentModeCoder) {
		return nil
	}
	expectedBranch := strings.TrimSpace(agentDef.Workspace.Git.ExpectedBranch)
	if expectedBranch == "" {
		return nil
	}
	workspaceRoot := strings.TrimSpace(agentDef.Workspace.Root)
	if workspaceRoot == "" {
		return fmt.Errorf("runtimeConfig.workspace.root is required when git.expectedBranch is set")
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

func (s *Server) buildSessionToolOverrides(agentDef catalog.AgentDefinition) map[string]api.ToolDetailResponse {
	overrides := cloneToolOverrides(agentDef.ToolOverrides)
	if !hasRuntimeSandbox(agentDef.Runtime) {
		return overrides
	}
	tool, ok := s.lookupInternalTool("bash_sandbox")
	if !ok {
		return overrides
	}
	override := cloneToolDetailResponse(tool)
	override.Name = "bash"
	override.Key = "bash"
	if overrides == nil {
		overrides = map[string]api.ToolDetailResponse{}
	}
	if existing, ok := overrides["bash"]; ok {
		override = applyToolOverride(override, map[string]api.ToolDetailResponse{"bash": existing})
	}
	overrides["bash"] = override
	return overrides
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
