package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
)

type querySessionBuildOptions struct {
	Created          bool
	IncludeHistory   bool
	IncludeMemory    bool
	AllowInvokeAgent bool
	Principal        *Principal
}

func (s *Server) BuildQuerySession(ctx context.Context, req api.QueryRequest, summary chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
	historyMessages := []map[string]any(nil)
	if options.IncludeHistory && s.deps.Chats != nil {
		historyMessages, _ = s.deps.Chats.LoadRawMessages(req.ChatID, s.deps.Config.ChatStorage.K)
	}

	var memoryContext string
	if options.IncludeMemory && s.deps.Memory != nil && req.Message != "" {
		topN := s.deps.Config.Memory.ContextTopN
		if topN <= 0 {
			topN = 5
		}
		maxChars := s.deps.Config.Memory.ContextMaxChars
		if maxChars <= 0 {
			maxChars = 4000
		}
		memories, _ := s.deps.Memory.Search(req.Message, topN)
		if len(memories) > 0 {
			var sb strings.Builder
			for _, mem := range memories {
				entry := fmt.Sprintf("id: %s\nsubjectKey: %s\nsourceType: %s\ncategory: %s\nimportance: %d\ntags: %s\ncontent: %s\n---\n",
					mem.ID, mem.SubjectKey, mem.SourceType, mem.Category, mem.Importance,
					strings.Join(mem.Tags, ","), mem.Summary)
				if sb.Len()+len(entry) > maxChars {
					break
				}
				sb.WriteString(entry)
			}
			memoryContext = sb.String()
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
		ToolNames:             buildSessionToolNames(agentDef.Tools, options.AllowInvokeAgent),
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
		MemoryContext:         memoryContext,
		RuntimeContext:        runtimeContext,
		PromptAppend:          promptAppend,
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

func buildSessionToolNames(base []string, allowInvokeAgent bool) []string {
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
		if !allowInvokeAgent && key == strings.ToLower(contracts.InvokeAgentToolName) {
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, name)
	}
	if allowInvokeAgent {
		key := strings.ToLower(contracts.InvokeAgentToolName)
		if _, ok := seen[key]; !ok {
			tools = append(tools, contracts.InvokeAgentToolName)
		}
	}
	return tools
}

func canUseInvokeAgentTool(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "REACT", "ONESHOT":
		return true
	default:
		return false
	}
}
