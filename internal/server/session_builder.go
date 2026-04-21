package server

import (
	"context"
	"log"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/memory"
)

type querySessionBuildOptions struct {
	Created           bool
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
	if options.IncludeMemory && s.deps.Memory != nil && req.Message != "" {
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
			AgentKey: req.AgentKey,
			TeamID:   req.TeamID,
			ChatID:   req.ChatID,
			UserKey:  userKey,
			Query:    req.Message,
			TopFacts: topN,
			TopObs:   topN,
			MaxChars: maxChars,
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

	promptAppend := buildPromptAppendConfig(agentDef)
	skillHookDirs, sandboxEnvOverrides := resolveSkillRuntimeSettings(sandboxAgentEnv(agentDef.Sandbox["env"]), agentDef.Skills, s.deps.Registry)
	log.Printf("[server][skill-runtime] agent=%s skills=%v hookDirs=%v sandboxEnvKeys=%v",
		agentDef.Key,
		agentDef.Skills,
		skillHookDirs,
		sortedStringKeys(sandboxEnvOverrides),
	)

	session := contracts.QuerySession{
		RequestID:             req.RequestID,
		RunID:                 req.RunID,
		ChatID:                req.ChatID,
		ChatName:              summary.ChatName,
		AgentKey:              req.AgentKey,
		AgentName:             agentDef.Name,
		ModelKey:              agentDef.ModelKey,
		ToolNames:             buildSessionToolNames(agentDef.Tools, options.AllowInvokeAgents),
		Mode:                  agentDef.Mode,
		TeamID:                req.TeamID,
		Created:               options.Created,
		SkillKeys:             append([]string(nil), agentDef.Skills...),
		ContextTags:           append([]string(nil), agentDef.ContextTags...),
		Budget:                contracts.CloneMap(agentDef.Budget),
		StageSettings:         contracts.CloneMap(agentDef.StageSettings),
		ToolOverrides:         cloneToolOverrides(agentDef.ToolOverrides),
		ResolvedBudget:        contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedStageSettings: contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask),
		HistoryMessages:       historyMessages,
		StableMemoryContext:   stableMemoryContext,
		SessionMemoryContext:  sessionMemoryContext,
		ObservationContext:    observationContext,
		MemoryUsageSummary:    memoryUsageSummary,
		RuntimeContext:        runtimeContext,
		PromptAppend:          promptAppend,
		StaticMemoryPrompt:    strings.TrimSpace(agentDef.StaticMemoryPrompt),
		MemoryPrompt:          agentDef.MemoryPrompt,
		SkillCatalogPrompt:    buildSkillCatalogPrompt(agentDef, s.deps.Registry, promptAppend),
		SoulPrompt:            agentDef.SoulPrompt,
		AgentsPrompt:          agentDef.AgentsPrompt,
		PlanPrompt:            agentDef.PlanPrompt,
		ExecutePrompt:         agentDef.ExecutePrompt,
		SummaryPrompt:         agentDef.SummaryPrompt,
		SandboxEnvironmentID:  extractSandboxField(agentDef.Sandbox, "environmentId"),
		SandboxLevel:          extractSandboxField(agentDef.Sandbox, "level"),
		SandboxExtraMounts:    sandboxExtraMounts(agentDef.Sandbox["extraMounts"]),
		SkillHookDirs:         skillHookDirs,
		SandboxEnvOverrides:   sandboxEnvOverrides,
	}
	if principal != nil {
		session.Subject = principal.Subject
	}
	return session, nil
}

func buildSessionToolNames(base []string, allowInvokeAgents bool) []string {
	tools := make([]string, 0, len(base)+1)
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
	if allowInvokeAgents {
		key := strings.ToLower(contracts.InvokeAgentsToolName)
		if _, ok := seen[key]; !ok {
			tools = append(tools, contracts.InvokeAgentsToolName)
		}
	}
	return tools
}

func canUseInvokeAgentsTool(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "REACT", "ONESHOT":
		return true
	default:
		return false
	}
}
