package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	agentcoder "agent-platform/internal/agent/coder"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

type SystemInitProfileBuilder struct {
	Models   *models.ModelRegistry
	Defaults SystemInitDefaults
}

type SystemInitDefaults struct {
	PlanMaxSteps             int
	PlanMaxWorkRoundsPerTask int
	Prompts                  config.PromptsConfig
}

func NewSystemInitProfileBuilder(models *models.ModelRegistry, defaults SystemInitDefaults) SystemInitProfileBuilder {
	return SystemInitProfileBuilder{Models: models, Defaults: defaults}
}

func (b SystemInitProfileBuilder) BuildSystemInitProfiles(input contracts.SystemInitBuildInput) ([]contracts.SystemInitProfile, error) {
	profiles := BuildSystemInitProfiles(
		input.Session,
		input.Request,
		input.ToolDefinitions,
		b.Defaults.PlanMaxSteps,
		b.Defaults.PlanMaxWorkRoundsPerTask,
		b.Defaults.Prompts,
	)
	if b.Models != nil {
		for i := range profiles {
			b.applyRequestProfile(&profiles[i], input.Session, input.Request)
		}
	}
	if err := validateSystemInitProfiles(profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func validateSystemInitProfiles(profiles []contracts.SystemInitProfile) error {
	if len(profiles) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(profiles))
	initialCount := 0
	for _, profile := range profiles {
		key := strings.TrimSpace(profile.CacheKey)
		if key == "" {
			return fmt.Errorf("system-init profile cache key is required")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate system-init cache key %q", key)
		}
		seen[key] = struct{}{}
		if profile.Initial {
			initialCount++
		}
	}
	if initialCount != 1 {
		return fmt.Errorf("system-init profiles require exactly one initial profile, got %d", initialCount)
	}
	return nil
}

func BuildSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, toolDefs []api.ToolDetailResponse, defaultPlanMaxSteps int, defaultPlanMaxWorkRoundsPerTask int, prompts config.PromptsConfig) []contracts.SystemInitProfile {
	toolDefs = mergeToolDefinitions(toolDefs, session.ModeToolDefinitions)
	mode := normalizedSystemInitMode(session.Mode)
	if session.PlanningMode {
		if mode != agentcoder.MainStage {
			return nil
		}
		settings := resolvePlanExecuteRuntimeSettings(session, defaultPlanMaxSteps, defaultPlanMaxWorkRoundsPerTask)
		session.ResolvedStageSettings = settings
		return buildCoderPlanningSystemInitProfiles(session, req, settings, toolDefs)
	}
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
	default:
		if descriptor, ok := agentbuiltin.Lookup(session.Mode); ok {
			return []contracts.SystemInitProfile{buildDefaultSystemInitProfile(session, req, toolDefs, descriptor.MainStage)}
		}
		stage := "react"
		if strings.TrimSpace(session.Mode) == "" {
			stage = "oneshot"
		}
		return []contracts.SystemInitProfile{buildDefaultSystemInitProfile(session, req, toolDefs, stage)}
	}
}

func (b SystemInitProfileBuilder) applyRequestProfile(profile *contracts.SystemInitProfile, session contracts.QuerySession, req api.QueryRequest) {
	if b.Models == nil || profile == nil {
		return
	}
	stage := profileRuntimeStage(session, *profile)
	stageSettings := stageSettingsForName(session.ResolvedStageSettings, stage)
	modelKey := strings.TrimSpace(stageSettings.ModelKey)
	if modelKey == "" {
		modelKey = strings.TrimSpace(session.ModelKey)
	}
	model, provider, err := b.Models.Get(modelKey)
	if err != nil {
		return
	}
	protocol := resolveProtocol(nil, model)
	if protocol == nil {
		return
	}
	protocolConfig := resolveProtocolRuntimeConfig(provider, model)
	toolSpecs := openAIToolSpecsFromAny(profile.Tools)
	messages := profileMessages(profile.SystemMessage)
	toolChoice := "auto"
	if strings.EqualFold(strings.TrimSpace(session.Mode), agentteam.Mode) && strings.EqualFold(strings.TrimSpace(stage), agentteam.MainStage) {
		toolChoice = "required"
	}
	prepared, err := protocol.PrepareRequest(protocolStreamParams{
		runID:          req.RunID,
		provider:       provider,
		model:          model,
		protocolConfig: protocolConfig,
		stageSettings:  stageSettings,
		messages:       messages,
		toolSpecs:      toolSpecs,
		toolChoice:     toolChoice,
	})
	if err != nil {
		return
	}
	profile.Model = modelSnapshotFromDefinition(model, provider, prepared.Endpoint, strings.TrimSpace(stageSettings.ReasoningEffort))
	profile.ToolChoice = effectiveTraceToolChoice(toolChoice, toolSpecs)
	profile.RequestOptions = requestOptionsFromPreparedBody(prepared.RequestBody)
}

func profileRuntimeStage(session contracts.QuerySession, profile contracts.SystemInitProfile) string {
	stage := strings.ToLower(strings.TrimSpace(profile.Stage))
	mode := strings.ToLower(strings.TrimSpace(profile.Mode))
	if stage == "" || stage == "main" {
		if descriptor, ok := agentbuiltin.Lookup(mode); ok {
			return descriptor.MainStage
		}
		switch mode {
		case "oneshot", "react":
			return mode
		}
		normalizedMode := normalizedSystemInitMode(session.Mode)
		if descriptor, ok := agentbuiltin.Lookup(normalizedMode); ok {
			return descriptor.MainStage
		}
		if normalizedMode == "oneshot" || normalizedMode == "react" {
			return normalizedMode
		}
		return "react"
	}
	if mode == agentcoder.MainStage {
		switch stage {
		case "plan":
			return agentcoder.PlanStage
		case "execute":
			return agentcoder.ExecuteStage
		}
	}
	return stage
}

func profileMessages(systemMessage map[string]any) []openAIMessage {
	msg := rawMessageToOpenAI(systemMessage, false)
	if msg.Role == "" {
		return []openAIMessage{{Role: "user", Content: ""}}
	}
	return []openAIMessage{
		msg,
		{Role: "user", Content: ""},
	}
}

func openAIToolSpecsFromAny(tools []any) []openAIToolSpec {
	if len(tools) == 0 {
		return nil
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return nil
	}
	var specs []openAIToolSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil
	}
	return specs
}

func modelSnapshotFromDefinition(model models.ModelDefinition, provider models.ProviderDefinition, endpoint string, reasoningEffort string) map[string]any {
	out := map[string]any{}
	if key := strings.TrimSpace(model.Key); key != "" {
		out["key"] = key
	}
	if id := strings.TrimSpace(model.ModelID); id != "" {
		out["id"] = id
	}
	if providerKey := strings.TrimSpace(provider.Key); providerKey != "" {
		out["providerKey"] = providerKey
	}
	protocol := strings.TrimSpace(model.Protocol)
	if protocol == "" {
		protocol = "OPENAI"
	}
	if protocol != "" {
		out["protocol"] = protocol
	}
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		out["endpoint"] = endpoint
	}
	if reasoningEffort != "" {
		out["reasoningEffort"] = reasoningEffort
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func SystemInitCacheKey(mode string, stage string) string {
	normalizedMode := normalizedSystemInitMode(mode)
	if normalizedMode == "plan-execute" {
		return normalizedMode + ":" + normalizedPlanExecuteStage(stage)
	}
	if normalizedMode == agentcoder.MainStage {
		return agentcoder.SystemInitCacheKey(stage)
	}
	if descriptor, ok := agentbuiltin.Lookup(mode); ok {
		return descriptor.MainCacheKey
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
		"modeSystemPrompt":       session.ModeSystemPrompt,
		"runtimeEnvironmentID":   session.RuntimeEnvironmentID,
		"runtimeLevel":           session.RuntimeLevel,
		"runtimeExtraMounts":     session.RuntimeExtraMounts,
		"agentHasRuntimeSandbox": session.AgentHasRuntimeSandbox,
		"agentHasMemoryConfig":   session.AgentHasMemoryConfig,
		"skillHookDirs":          sortedStrings(session.SkillHookDirs),
		"runtimeEnvOverrides":    session.RuntimeEnvOverrides,
		"toolDefinitions":        stableToolDefinitions(toolDefs),
		"teamRuntime":            session.TeamRuntime,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func buildDefaultSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, toolDefs []api.ToolDetailResponse, stage string) contracts.SystemInitProfile {
	toolNames := coderRuntimeToolNamesForStage(session, stage, session.ToolNames)
	effectiveDefs := effectiveToolDefinitions(toolDefs, toolNames, session.AgentHasRuntimeSandbox)
	systemPrompt := buildSystemPrompt(session, req, session.ModelKey, PromptBuildOptions{
		Stage:                 stage,
		ToolDefinitions:       effectiveDefs,
		IncludeAfterCallHints: true,
	})
	specs := toOpenAIToolSpecs(effectiveDefs)
	profile := contracts.SystemInitProfile{
		AgentKey:      strings.TrimSpace(session.AgentKey),
		CacheKey:      SystemInitCacheKey(session.Mode, stage),
		Mode:          normalizedSystemInitMode(session.Mode),
		Stage:         "main",
		Fingerprint:   ComputeSystemInitFingerprint(session, "main", effectiveDefs),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         openAIToolSpecsToAny(specs),
		Initial:       true,
	}
	if spec, ok := agentbuiltin.MainSystemInitSpec(session.Mode); ok {
		profile.CacheKey = spec.CacheKey
		profile.Mode = spec.Mode
		profile.Stage = spec.Stage
		profile.Initial = spec.Initial
	}
	return profile
}

func buildPlanSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	tools := planSystemInitTools(settings.Plan)
	effectiveDefs := effectiveToolDefinitions(toolDefs, tools, session.AgentHasRuntimeSandbox)
	systemPrompt := buildSystemPrompt(session, req, session.ModelKey, PromptBuildOptions{
		Stage:                 "plan",
		ToolDefinitions:       effectiveDefs,
		IncludeAfterCallHints: true,
	})
	specs := toOpenAIToolSpecs(effectiveDefs)
	return contracts.SystemInitProfile{
		AgentKey:      strings.TrimSpace(session.AgentKey),
		CacheKey:      SystemInitCacheKey(session.Mode, "plan"),
		Mode:          "plan-execute",
		Stage:         "plan",
		Fingerprint:   ComputeSystemInitFingerprint(session, "plan", effectiveDefs),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         openAIToolSpecsToAny(specs),
		Initial:       true,
	}
}

func buildExecuteSystemInitProfile(session contracts.QuerySession, settings contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	tools := appendUniqueTools(stageToolsOrDefault(settings.Execute, session.ToolNames), "plan_update_task")
	effectiveDefs := effectiveToolDefinitions(toolDefs, tools, session.AgentHasRuntimeSandbox)
	systemPrompt := strings.TrimSpace(settings.Execute.PrimaryPrompt())
	if systemPrompt == "" {
		systemPrompt = "Execute the current task."
	}
	specs := toOpenAIToolSpecs(effectiveDefs)
	return contracts.SystemInitProfile{
		AgentKey:      strings.TrimSpace(session.AgentKey),
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
		AgentKey:      strings.TrimSpace(session.AgentKey),
		CacheKey:      SystemInitCacheKey(session.Mode, "summary"),
		Mode:          "plan-execute",
		Stage:         "summary",
		Fingerprint:   ComputeSystemInitFingerprint(fingerprintSession, "summary", nil),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         []any{},
	}
}

func buildCoderPlanningSystemInitProfiles(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) []contracts.SystemInitProfile {
	specs := agentcoder.PlanningSystemInitSpecs(session, req, settings)
	profiles := make([]contracts.SystemInitProfile, 0, len(specs))
	for _, spec := range specs {
		profiles = append(profiles, buildCoderPlanningSystemInitProfile(session, req, spec, toolDefs))
	}
	return profiles
}

func buildCoderPlanningExecuteSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, settings contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	return buildCoderPlanningSystemInitProfile(session, req, agentcoder.PlanningExecuteSystemInitSpec(session, req, settings), toolDefs)
}

func buildCoderPlanningPlanSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, _ contracts.PlanExecuteSettings, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	return buildCoderPlanningSystemInitProfile(session, req, agentcoder.PlanningPlanSystemInitSpec(), toolDefs)
}

func buildCoderPlanningSystemInitProfile(session contracts.QuerySession, req api.QueryRequest, spec agentcontract.SystemInitSpec, toolDefs []api.ToolDetailResponse) contracts.SystemInitProfile {
	effectiveDefs := effectiveToolDefinitions(toolDefs, spec.ToolNames, session.AgentHasRuntimeSandbox)
	systemPrompt := strings.TrimSpace(spec.SystemPrompt)
	if spec.UseSharedSystemPrompt {
		systemPrompt = buildSystemPrompt(session, req, session.ModelKey, PromptBuildOptions{
			Stage:                 spec.PromptStage,
			ToolDefinitions:       effectiveDefs,
			IncludeAfterCallHints: spec.IncludeAfterCallHints,
		})
	}
	specs := toOpenAIToolSpecs(effectiveDefs)
	return contracts.SystemInitProfile{
		AgentKey:      strings.TrimSpace(session.AgentKey),
		CacheKey:      spec.CacheKey,
		Mode:          spec.Mode,
		Stage:         spec.Stage,
		Fingerprint:   ComputeSystemInitFingerprint(session, spec.FingerprintStage, effectiveDefs),
		SystemMessage: map[string]any{"role": "system", "content": systemPrompt},
		Tools:         openAIToolSpecsToAny(specs),
		Initial:       spec.Initial,
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
	if descriptor, ok := agentbuiltin.Lookup(mode); ok {
		return strings.ToLower(descriptor.NormalizedMode())
	}
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ONESHOT":
		return "oneshot"
	case "PLAN_EXECUTE", "PLAN-EXECUTE":
		return "plan-execute"
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
