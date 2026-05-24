package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type SystemInitProfileBuilder struct{}

func (SystemInitProfileBuilder) BuildSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, toolDefs []api.ToolDetailResponse, defaultPlanMaxSteps int, defaultPlanMaxWorkRoundsPerTask int, prompts config.PromptsConfig) []contracts.SystemInitProfile {
	return BuildSystemInitProfiles(session, req, toolDefs, defaultPlanMaxSteps, defaultPlanMaxWorkRoundsPerTask, prompts)
}

func BuildSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, toolDefs []api.ToolDetailResponse, defaultPlanMaxSteps int, defaultPlanMaxWorkRoundsPerTask int, prompts config.PromptsConfig) []contracts.SystemInitProfile {
	if session.PlanningMode {
		return nil
	}
	mode := normalizedSystemInitMode(session.Mode)
	switch mode {
	case "plan-execute":
		settings := resolvePlanExecuteRuntimeSettings(session, defaultPlanMaxSteps, defaultPlanMaxWorkRoundsPerTask)
		session.ResolvedStageSettings = settings
		return []contracts.SystemInitProfile{
			buildPlanSystemInitProfile(session, req, settings, toolDefs),
			buildExecuteSystemInitProfile(session, settings, toolDefs),
			buildSummarySystemInitProfile(session, settings, prompts),
		}
	case "oneshot":
		return []contracts.SystemInitProfile{buildDefaultSystemInitProfile(session, req, toolDefs, "oneshot")}
	case "coder":
		return []contracts.SystemInitProfile{buildDefaultSystemInitProfile(session, req, toolDefs, "coder")}
	default:
		stage := "react"
		if strings.TrimSpace(session.Mode) == "" {
			stage = "oneshot"
		}
		return []contracts.SystemInitProfile{buildDefaultSystemInitProfile(session, req, toolDefs, stage)}
	}
}

func SystemInitCacheKey(mode string, stage string) string {
	normalizedMode := normalizedSystemInitMode(mode)
	if normalizedMode == "plan-execute" {
		return normalizedMode + ":" + normalizedPlanExecuteStage(stage)
	}
	return normalizedMode + ":main"
}

func ComputeSystemInitFingerprint(session contracts.QuerySession, stage string, toolDefs []api.ToolDetailResponse) string {
	payload := map[string]any{
		"agentKey":               session.AgentKey,
		"agentName":              session.AgentName,
		"agentRole":              session.AgentRole,
		"agentDescription":       session.AgentDescription,
		"mode":                   normalizedSystemInitMode(session.Mode),
		"stage":                  strings.TrimSpace(stage),
		"modelKey":               session.ModelKey,
		"toolNames":              sortedStrings(session.ToolNames),
		"skillKeys":              sortedStrings(session.SkillKeys),
		"contextTags":            sortedStrings(session.ContextTags),
		"budget":                 session.Budget,
		"stageSettings":          session.StageSettings,
		"resolvedStageSettings":  session.ResolvedStageSettings,
		"promptAppend":           session.PromptAppend,
		"staticMemoryPrompt":     session.StaticMemoryPrompt,
		"skillCatalogPrompt":     session.SkillCatalogPrompt,
		"soulPrompt":             session.SoulPrompt,
		"agentsPrompt":           session.AgentsPrompt,
		"workspaceAgentsPrompt":  session.WorkspaceAgentsPrompt,
		"planPrompt":             session.PlanPrompt,
		"executePrompt":          session.ExecutePrompt,
		"summaryPrompt":          session.SummaryPrompt,
		"runtimeEnvironmentID":   session.RuntimeEnvironmentID,
		"runtimeLevel":           session.RuntimeLevel,
		"runtimeExtraMounts":     session.RuntimeExtraMounts,
		"agentHasRuntimeSandbox": session.AgentHasRuntimeSandbox,
		"agentHasMemoryConfig":   session.AgentHasMemoryConfig,
		"skillHookDirs":          sortedStrings(session.SkillHookDirs),
		"runtimeEnvOverrides":    session.RuntimeEnvOverrides,
		"toolDefinitions":        stableToolDefinitions(toolDefs),
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func buildDefaultSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, toolDefs []api.ToolDetailResponse, stage string) contracts.SystemInitProfile {
	effectiveDefs := applyToolOverrides(filterToolDefinitions(toolDefs, session.ToolNames), session.ToolOverrides)
	systemPrompt := buildSystemPrompt(session, req, session.ModelKey, PromptBuildOptions{
		Stage:                 stage,
		ToolDefinitions:       effectiveDefs,
		IncludeAfterCallHints: true,
	})
	specs := toOpenAIToolSpecs(effectiveDefs)
	return contracts.SystemInitProfile{
		CacheKey:      SystemInitCacheKey(session.Mode, stage),
		Mode:          normalizedSystemInitMode(session.Mode),
		Stage:         "main",
		Fingerprint:   ComputeSystemInitFingerprint(session, "main", effectiveDefs),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         openAIToolSpecsToAny(specs),
	}
}

func buildPlanSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	tools := planSystemInitTools(settings.Plan)
	effectiveDefs := applyToolOverrides(filterToolDefinitions(toolDefs, tools), session.ToolOverrides)
	systemPrompt := buildSystemPrompt(session, req, session.ModelKey, PromptBuildOptions{
		Stage:                 "plan",
		ToolDefinitions:       effectiveDefs,
		IncludeAfterCallHints: true,
	})
	specs := toOpenAIToolSpecs(effectiveDefs)
	return contracts.SystemInitProfile{
		CacheKey:      SystemInitCacheKey(session.Mode, "plan"),
		Mode:          "plan-execute",
		Stage:         "plan",
		Fingerprint:   ComputeSystemInitFingerprint(session, "plan", effectiveDefs),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         openAIToolSpecsToAny(specs),
	}
}

func buildExecuteSystemInitProfile(session contracts.QuerySession, settings contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	tools := appendUniqueTools(stageToolsOrDefault(settings.Execute, session.ToolNames), "plan_update_task")
	effectiveDefs := applyToolOverrides(filterToolDefinitions(toolDefs, tools), session.ToolOverrides)
	systemPrompt := strings.TrimSpace(settings.Execute.PrimaryPrompt())
	if systemPrompt == "" {
		systemPrompt = "Execute the current task."
	}
	specs := toOpenAIToolSpecs(effectiveDefs)
	return contracts.SystemInitProfile{
		CacheKey:      SystemInitCacheKey(session.Mode, "execute"),
		Mode:          "plan-execute",
		Stage:         "execute",
		Fingerprint:   ComputeSystemInitFingerprint(session, "execute", effectiveDefs),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         openAIToolSpecsToAny(specs),
	}
}

func buildSummarySystemInitProfile(session contracts.QuerySession, settings contracts.PlanExecuteSettings, prompts config.PromptsConfig) contracts.SystemInitProfile {
	systemPrompt := strings.TrimSpace(settings.Summary.PrimaryPrompt())
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(prompts.PlanExecute.SummarySystemPrompt)
	}
	if systemPrompt == "" {
		systemPrompt = defaultPlanSummarySystemPrompt
	}
	fingerprintSession := session
	fingerprintSession.SummaryPrompt = strings.TrimSpace(strings.Join([]string{fingerprintSession.SummaryPrompt, systemPrompt}, "\n"))
	return contracts.SystemInitProfile{
		CacheKey:      SystemInitCacheKey(session.Mode, "summary"),
		Mode:          "plan-execute",
		Stage:         "summary",
		Fingerprint:   ComputeSystemInitFingerprint(fingerprintSession, "summary", nil),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         []any{},
	}
}

func resolvePlanExecuteRuntimeSettings(session contracts.QuerySession, defaultMaxSteps int, defaultMaxWorkRoundsPerTask int) contracts.PlanExecuteSettings {
	settings := session.ResolvedStageSettings
	if settings.MaxSteps <= 0 || settings.MaxWorkRoundsPerTask <= 0 {
		settings = contracts.ResolvePlanExecuteSettings(session.StageSettings, defaultMaxSteps, defaultMaxWorkRoundsPerTask)
	}
	return settings
}

func planSystemInitTools(stage contracts.StageSettings) []string {
	if len(stage.Tools) > 0 {
		return appendUniqueTools(stage.Tools, "plan_add_tasks")
	}
	return []string{"plan_add_tasks"}
}

func normalizedSystemInitMode(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ONESHOT":
		return "oneshot"
	case "PLAN_EXECUTE":
		return "plan-execute"
	case "CODER":
		return "coder"
	default:
		return "react"
	}
}

func normalizedPlanExecuteStage(stage string) string {
	value := strings.ToLower(strings.TrimSpace(stage))
	switch {
	case value == "plan":
		return "plan"
	case value == "summary":
		return "summary"
	case strings.HasPrefix(value, "execute"):
		return "execute"
	default:
		return "execute"
	}
}

func stableToolDefinitions(defs []api.ToolDetailResponse) []api.ToolDetailResponse {
	out := append([]api.ToolDetailResponse(nil), defs...)
	sort.Slice(out, func(i, j int) bool {
		left := strings.TrimSpace(out[i].Name)
		if left == "" {
			left = strings.TrimSpace(out[i].Key)
		}
		right := strings.TrimSpace(out[j].Name)
		if right == "" {
			right = strings.TrimSpace(out[j].Key)
		}
		if left == right {
			return strings.TrimSpace(out[i].Key) < strings.TrimSpace(out[j].Key)
		}
		return left < right
	})
	return out
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func openAIToolSpecsToAny(specs []openAIToolSpec) []any {
	if len(specs) == 0 {
		return []any{}
	}
	raw, err := json.Marshal(specs)
	if err != nil {
		return nil
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func cachedToolSpecsToOpenAI(raw []any) ([]openAIToolSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var specs []openAIToolSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func cachedSystemMessageToOpenAI(raw map[string]any) (openAIMessage, bool) {
	if len(raw) == 0 {
		return openAIMessage{}, false
	}
	role, _ := raw["role"].(string)
	if strings.TrimSpace(role) != "system" {
		return openAIMessage{}, false
	}
	return openAIMessage{
		Role:    "system",
		Content: raw["content"],
	}, true
}

func resolveCachedSystemInit(session contracts.QuerySession, cacheKey string) (openAIMessage, []openAIToolSpec, bool) {
	if len(session.SystemInitCache) == 0 {
		return openAIMessage{}, nil, false
	}
	snapshot, ok := session.SystemInitCache[cacheKey]
	if !ok {
		return openAIMessage{}, nil, false
	}
	systemMessage, ok := cachedSystemMessageToOpenAI(snapshot.SystemMessage)
	if !ok {
		log.Printf("[llm][run:%s][system-init] invalid cached system message cacheKey=%s", session.RunID, cacheKey)
		return openAIMessage{}, nil, false
	}
	toolSpecs, err := cachedToolSpecsToOpenAI(snapshot.Tools)
	if err != nil {
		log.Printf("[llm][run:%s][system-init] invalid cached tool specs cacheKey=%s err=%v", session.RunID, cacheKey, err)
		return openAIMessage{}, nil, false
	}
	return systemMessage, toolSpecs, true
}

func replaceSystemMessage(messages []openAIMessage, system openAIMessage) []openAIMessage {
	out := append([]openAIMessage(nil), messages...)
	for index := range out {
		if strings.TrimSpace(out[index].Role) == "system" {
			out[index] = system
			return out
		}
	}
	return append([]openAIMessage{system}, out...)
}
