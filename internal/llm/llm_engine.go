package llm

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	. "agent-platform/internal/models"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/toolinteraction"
)

type LLMAgentEngine struct {
	cfg          config.Config
	models       *ModelRegistry
	tools        ToolExecutor
	interactions *toolinteraction.Registry
	sandbox      SandboxClient
	httpClient   *http.Client
}

type runStreamOptions struct {
	ExecCtx                      *ExecutionContext
	Messages                     []openAIMessage
	ToolNames                    []string
	ModelKey                     string
	MaxSteps                     int
	Stage                        string
	ToolChoice                   string
	RequireTeamDelegation        bool
	PreserveProvidedSystemPrompt bool
	PostToolHook                 func(toolName string, toolID string) PostToolHookResult
	DisableContextCompaction     bool
	DisableRunControl            bool
}

func NewLLMAgentEngine(cfg config.Config, models *ModelRegistry, tools ToolExecutor, interactions *toolinteraction.Registry, sandbox SandboxClient) *LLMAgentEngine {
	return NewLLMAgentEngineWithHTTPClient(cfg, models, tools, interactions, sandbox, nil)
}

func NewLLMAgentEngineWithHTTPClient(cfg config.Config, models *ModelRegistry, tools ToolExecutor, interactions *toolinteraction.Registry, sandbox SandboxClient, httpClient *http.Client) *LLMAgentEngine {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &LLMAgentEngine{
		cfg:          cfg,
		models:       models,
		tools:        tools,
		interactions: interactions,
		sandbox:      sandbox,
		httpClient:   httpClient,
	}
}

func (e *LLMAgentEngine) Stream(ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return resolveAgentMode(session.Mode).Start(e, ctx, req, session)
}

func (e *LLMAgentEngine) newRunStream(ctx context.Context, req api.QueryRequest, session QuerySession, allowToolUse bool) (AgentStream, error) {
	stage := strings.ToLower(session.Mode)
	if stage == "" {
		stage = "oneshot"
	}
	return e.newRunStreamWithOptions(ctx, req, session, allowToolUse, runStreamOptions{Stage: stage})
}

func (e *LLMAgentEngine) newRunStreamWithOptions(ctx context.Context, req api.QueryRequest, session QuerySession, allowToolUse bool, options runStreamOptions) (AgentStream, error) {
	modelKey := session.ModelKey
	if strings.TrimSpace(options.ModelKey) != "" {
		modelKey = strings.TrimSpace(options.ModelKey)
	}
	if strings.TrimSpace(modelKey) == "" {
		return nil, errors.New("modelConfig.modelKey is required")
	}
	model, provider, err := e.models.Get(modelKey)
	if err != nil {
		return nil, err
	}
	protocolConfig := resolveProtocolRuntimeConfig(provider, model)
	stageSettings := stageSettingsForSession(session, options.Stage)
	budgetStage := budgetStageForName(session, options.Stage)
	allowedTools := resolveAllowedToolNames(session, options.Stage, options.ToolNames)
	allToolDefs := mergeToolDefinitions(e.tools.Definitions(), session.ModeToolDefinitions)
	effectiveDefs := effectiveToolDefinitions(allToolDefs, allowedTools, session)
	toolSpecs := toOpenAIToolSpecs(effectiveDefs)
	execCtx := options.ExecCtx
	if execCtx == nil {
		execCtx = &ExecutionContext{
			Request:               req,
			Session:               session,
			Budget:                session.ResolvedBudget,
			PlanExecuteSettings:   session.ResolvedPlanExecuteSettings,
			CoderPlanningSettings: session.ResolvedCoderPlanningSettings,
			RunLimits:             session.RunLimits,
			AccessLevel:           session.AccessLevel,
			ToolExecutionPolicy:   session.ToolExecutionPolicy,
			RunLoopState:          RunLoopStateIdle,
		}
	}
	execCtx.Request = req
	execCtx.Session = session
	execCtx.AccessLevel = session.AccessLevel
	execCtx.ToolExecutionPolicy = session.ToolExecutionPolicy
	execCtx.RunLimits = session.RunLimits
	if len(execCtx.StaticRuntimeEnv) == 0 {
		execCtx.StaticRuntimeEnv = CloneStringMap(session.StaticRuntimeEnv)
	}
	if execCtx.RunEnvironment == nil {
		execCtx.RunEnvironment = session.RunEnvironment
	}
	if execCtx.RunControl == nil && !options.DisableRunControl {
		execCtx.RunControl = RunControlFromContext(ctx)
	}
	if execCtx.Budget.Timeout <= 0 || execCtx.Budget.MaxSteps <= 0 {
		execCtx.Budget = NormalizeBudget(session.ResolvedBudget)
	}
	if execCtx.StartedAt.IsZero() {
		execCtx.StartedAt = time.Now()
	}
	e.restorePlanTasksForRun(execCtx, &session, options.Stage, effectiveDefs)
	cacheKey := SystemInitCacheKey(session.Mode, options.Stage)
	cachedSystem, cachedTools, cacheOK := resolveCachedSystemInit(session, cacheKey)
	if cacheOK && !cachedSystemInitHasPlanTaskContext(cachedSystem, session.PlanTaskContext) {
		cacheOK = false
	}
	if cacheOK && !cachedToolsCompatibleWithStageOverride(options.ToolNames, cachedTools) {
		cacheOK = false
	}
	useCachedSystemInit := cacheOK && !(len(options.Messages) > 0 && options.PreserveProvidedSystemPrompt)
	if useCachedSystemInit {
		toolSpecs = cachedTools
	}
	if execCtx.RunControl != nil {
		execCtx.RunControl.TransitionState(RunLoopStateModelStreaming)
	}
	messages := options.Messages
	pinnedMessageStart := -1
	pinnedMessageEnd := -1
	if len(messages) == 0 {
		if useCachedSystemInit {
			messages = []openAIMessage{cachedSystem}
		} else {
			systemPrompt := buildSystemPrompt(session, req, model.Key, PromptBuildOptions{
				Stage:                   options.Stage,
				StageInstructionsPrompt: "",
				StageSystemPrompt:       "",
				ToolDefinitions:         effectiveDefs,
				IncludeAfterCallHints:   true,
			})
			e.logPromptMemory(session.RunID, options.Stage, req, session)
			if e.llmConsoleEnabled(llmConsolePrompt) {
				log.Printf("[llm][run:%s][%s] LLM delta stream system prompt:\n%s", session.RunID, options.Stage, systemPrompt)
			}
			messages = []openAIMessage{{
				Role:    "system",
				Content: systemPrompt,
			}}
		}
		preserveReasoning := preserveReasoningContent(protocolConfig, stageSettings)
		for _, raw := range mergeRawMessagesByMsgID(session.HistoryMessages) {
			msg := rawMessageToOpenAI(raw, preserveReasoning)
			if msg.Role != "" {
				messages = append(messages, msg)
			}
		}
		currentMessages := session.CurrentMessages
		if len(currentMessages) == 0 {
			currentMessages = e.buildCurrentMessagesForRequest(req, session, model.IsVision)
		}
		pinnedMessageStart = len(messages)
		for _, raw := range currentMessages {
			msg := rawMessageToOpenAI(raw, preserveReasoning)
			if msg.Role != "" {
				messages = append(messages, msg)
			}
		}
		pinnedMessageEnd = len(messages)
	} else if useCachedSystemInit {
		messages = replaceSystemMessage(messages, cachedSystem)
	}
	if pinnedMessageStart < 0 && len(session.CurrentMessages) > 0 && len(session.CurrentMessages) <= len(messages) {
		pinnedMessageEnd = len(messages)
		pinnedMessageStart = pinnedMessageEnd - len(session.CurrentMessages)
	}
	maxSteps := options.MaxSteps
	if stageMaxSteps := budgetStageMaxSteps(session.ResolvedBudget, budgetStage); stageMaxSteps > 0 {
		maxSteps = stageMaxSteps
	} else if maxSteps <= 0 {
		maxSteps = e.resolveMaxSteps(session, budgetStage)
	}
	if limit := session.RunLimits.MaxToolRounds; limit > 0 {
		maxSteps = minPositive(maxSteps, limit)
	}

	toolChoice := strings.TrimSpace(strings.ToLower(options.ToolChoice))
	if toolChoice == "" {
		toolChoice = "auto"
	}
	promptBuildOptions := PromptBuildOptions{
		Stage:                   options.Stage,
		StageInstructionsPrompt: "",
		StageSystemPrompt:       "",
		ToolDefinitions:         effectiveDefs,
		IncludeAfterCallHints:   true,
	}
	stream := &llmRunStream{
		engine:               e,
		protocol:             resolveProtocol(e, model),
		ctx:                  ctx,
		req:                  req,
		session:              session,
		runControl:           execCtx.RunControl,
		model:                model,
		provider:             provider,
		toolSpecs:            toolSpecs,
		requestedToolNames:   append([]string(nil), allowedTools...),
		messages:             append([]openAIMessage(nil), messages...),
		pinnedMessageStart:   pinnedMessageStart,
		pinnedMessageEnd:     pinnedMessageEnd,
		compactDisabled:      options.DisableContextCompaction || strings.HasPrefix(strings.TrimSpace(session.RunScopeID), "btw:"),
		protocolConfig:       protocolConfig,
		stageSettings:        stageSettings,
		execCtx:              execCtx,
		maxSteps:             maxSteps,
		budgetStage:          budgetStage,
		toolChoice:           toolChoice,
		teamDelegateRequired: options.RequireTeamDelegation,
		postToolHook:         options.PostToolHook,
		allowToolUse:         allowToolUse,
		promptBuildOptions:   promptBuildOptions,
		onApprovalSummary:    approvalSummarySinkFromContext(ctx),
		systemInitCacheKey:   cacheKey,
		systemInitCacheUsed:  useCachedSystemInit,
	}
	if stream.runControl != nil && session.SupportsContextCompaction && strings.TrimSpace(session.SubTaskID) == "" && !options.DisableContextCompaction {
		stream.runControl.EnableContextCompact()
	}
	stream.syncAccessLevelFromRunControl()
	if len(session.SkillHookDirs) > 0 {
		if e.llmConsoleEnabled(llmConsoleHitl) {
			log.Printf("[llm][run:%s][hitl] creating SkillChecker hookDirs=%v", session.RunID, session.SkillHookDirs)
		}
		checker, err := hitl.NewSkillChecker(session.SkillHookDirs)
		if err != nil {
			log.Printf("[llm][run:%s][hitl][warning] failed to create SkillChecker hookDirs=%v err=%v", session.RunID, session.SkillHookDirs, err)
			return nil, err
		}
		stream.checker = checker
		if e.llmConsoleEnabled(llmConsoleHitl) {
			log.Printf("[llm][run:%s][hitl] SkillChecker enabled hookDirCount=%d", session.RunID, len(session.SkillHookDirs))
		}
	} else {
		if e.llmConsoleEnabled(llmConsoleHitl) {
			log.Printf("[llm][run:%s][hitl] SkillChecker disabled hookDirCount=0", session.RunID)
		}
	}
	if !stream.allowToolUse {
		stream.toolSpecs = nil
		stream.maxSteps = 1
	}
	if err := stream.prepareNextTurn(); err != nil {
		stream.Close()
		return nil, err
	}
	if err := stream.prime(); err != nil && !errors.Is(err, io.EOF) {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

func (e *LLMAgentEngine) buildCurrentMessagesForRequest(req api.QueryRequest, session QuerySession, fallbackVision bool) []map[string]any {
	isVision := fallbackVision
	if e != nil && e.models != nil {
		if model, err := e.models.GetModel(session.ModelKey); err == nil {
			isVision = model.IsVision
		}
	}
	return querymessages.BuildMessagesWithOptions(e.cfg.Paths.ChatsDir, req.ChatID, req.Role, req.Message, req.References, isVision, e.llmConsoleEnabled(llmConsoleMedia), querymessages.BuildOptions{
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

func (e *LLMAgentEngine) resolveMaxSteps(session QuerySession, budgetStage string) int {
	if budgetStageMaxSteps(session.ResolvedBudget, budgetStage) > 0 {
		return budgetStageMaxSteps(session.ResolvedBudget, budgetStage)
	}
	maxSteps := NormalizeBudget(session.ResolvedBudget).MaxSteps
	if maxSteps <= 0 {
		maxSteps = e.cfg.Defaults.React.MaxSteps
	}
	if maxSteps <= 0 {
		return 100
	}
	return maxSteps
}

func budgetStageMaxSteps(budget Budget, stage string) int {
	budget = NormalizeBudget(budget)
	if stageBudget, ok := budget.Stages[normalizeBudgetStageName(stage)]; ok && stageBudget.MaxSteps > 0 {
		return stageBudget.MaxSteps
	}
	return 0
}

func budgetStageForName(session QuerySession, stage string) string {
	normalized := normalizeBudgetStageName(stage)
	if strings.Contains(normalized, "summary") {
		return "summary"
	}
	if strings.Contains(normalized, "planning") {
		return "planning"
	}
	if strings.Contains(normalized, "plan") {
		return "plan"
	}
	if strings.Contains(normalized, "execute") || normalized == agentcoder.MainStage {
		return "execute"
	}
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(session.Mode))
	}
	switch normalized {
	case agentcoder.MainStage:
		return "execute"
	case "react", "oneshot", "":
		return "react"
	default:
		return normalized
	}
}

func normalizeBudgetStageName(stage string) string {
	return strings.ToLower(strings.TrimSpace(stage))
}

func stageSettingsForSession(session QuerySession, stage string) StageSettings {
	if agentcoder.IsMode(session.Mode) {
		normalized := strings.ToLower(strings.TrimSpace(stage))
		if session.PlanningMode || strings.HasPrefix(normalized, "coder-") || normalized == agentcoder.MainStage {
			if strings.Contains(normalized, "planning") {
				return session.ResolvedCoderPlanningSettings.Planning
			}
			return session.ResolvedCoderPlanningSettings.Execute
		}
	}
	return planExecuteStageSettingsForName(session.ResolvedPlanExecuteSettings, stage)
}

func planExecuteStageSettingsForName(settings PlanExecuteSettings, stage string) StageSettings {
	normalized := strings.ToLower(strings.TrimSpace(stage))
	switch {
	case strings.Contains(normalized, "summary"):
		return settings.Summary
	case strings.Contains(normalized, "plan"):
		return settings.Plan
	default:
		return settings.Execute
	}
}

func filterToolDefinitions(defs []api.ToolDetailResponse, allowed []string) []api.ToolDetailResponse {
	if len(allowed) == 0 {
		return nil
	}
	allowedSet := map[string]struct{}{}
	for _, name := range allowed {
		if strings.TrimSpace(name) != "" {
			allowedSet[strings.TrimSpace(name)] = struct{}{}
		}
	}
	filtered := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		if _, ok := allowedSet[def.Name]; ok {
			filtered = append(filtered, def)
			continue
		}
		if _, ok := allowedSet[def.Key]; ok {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

func resolveAllowedToolNames(session QuerySession, stage string, override []string) []string {
	if override != nil {
		if len(override) == 0 {
			return nil
		}
		return coderRuntimeToolNamesForStage(session, stage, override)
	}
	return coderRuntimeToolNamesForStage(session, stage, session.ToolNames)
}

func cachedToolsCompatibleWithStageOverride(override []string, cached []openAIToolSpec) bool {
	return override == nil || len(override) > 0 || len(cached) == 0
}

func effectiveToolDefinitions(defs []api.ToolDetailResponse, allowed []string, session QuerySession) []api.ToolDetailResponse {
	filtered := filterToolDefinitions(defs, allowed)
	if session.AgentHasRuntimeSandbox {
		if sandboxBash, ok := sandboxBashAsPublicBash(defs); ok {
			out := make([]api.ToolDetailResponse, 0, len(filtered))
			for _, def := range filtered {
				if isToolDefinitionNamed(def, "bash_sandbox") || isToolDefinitionNamed(def, "_sandbox_bash_") {
					continue
				}
				if isToolDefinitionNamed(def, "bash") {
					out = append(out, sandboxBash)
					continue
				}
				out = append(out, def)
			}
			filtered = out
		}
	}

	out := cloneToolDefinitions(filtered)
	if !sessionHasWorkspace(session) {
		for index := range out {
			hardenWorkspaceLessToolDefinition(&out[index])
		}
	}
	return out
}

func sessionHasWorkspace(session QuerySession) bool {
	if strings.TrimSpace(session.WorkspaceRoot) != "" ||
		strings.TrimSpace(session.RuntimeContext.LocalPaths.WorkspaceDir) != "" {
		return true
	}
	if session.AgentHasRuntimeSandbox || session.RuntimeContext.SandboxContext != nil {
		return strings.TrimSpace(session.RuntimeContext.SandboxPaths.WorkspaceDir) != ""
	}
	return false
}

func cloneToolDefinitions(defs []api.ToolDetailResponse) []api.ToolDetailResponse {
	out := make([]api.ToolDetailResponse, len(defs))
	for index, def := range defs {
		out[index] = def
		out[index].Parameters = cloneToolSchemaMap(def.Parameters)
		out[index].OutputSchema = cloneToolSchemaMap(def.OutputSchema)
		out[index].Meta = cloneToolSchemaMap(def.Meta)
	}
	return out
}

func cloneToolSchemaMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = cloneToolSchemaValue(value)
	}
	return out
}

func cloneToolSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneToolSchemaMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneToolSchemaValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func hardenWorkspaceLessToolDefinition(def *api.ToolDetailResponse) {
	if def == nil {
		return
	}
	switch normalizedToolDefinitionName(*def) {
	case "bash":
		requireToolParameter(def.Parameters, "cwd")
		setToolParameterDescription(def.Parameters, "cwd",
			"Required because this run has no Workspace. Use @chat for the current Chat working directory or @temp for temporary work; use another explicit semantic root only when the task targets it. Omitting cwd returns workspace_unavailable.")
		appendToolDefinitionNote(def, "This run has no Workspace. Every call must pass an explicit cwd, normally @chat.")
	case "file_glob":
		requireToolParameter(def.Parameters, "path")
		setToolParameterDescription(def.Parameters, "path",
			"Required because this run has no Workspace. Use @chat to search the current Chat directory, @temp for temporary files, or another explicit semantic root or absolute path. Relative paths and @workspace return workspace_unavailable.")
	case "file_grep":
		requireToolParameter(def.Parameters, "path")
		setToolParameterDescription(def.Parameters, "path",
			"Required because this run has no Workspace. Use @chat to search the current Chat directory, @temp for temporary files, or another explicit semantic root or absolute path. Relative paths and @workspace return workspace_unavailable.")
	case "file_read", "file_write", "file_edit":
		setToolParameterDescription(def.Parameters, "file_path",
			"Required. This run has no Workspace, so relative paths and @workspace are unavailable. Use an explicit @chat, @agent, @skills, @skills-center, @owner, or @temp path, or an allowed absolute path.")
	case "artifact_publish":
		setNestedToolParameterDescription(def.Parameters, []string{"properties", "artifacts", "items", "properties", "path"},
			"Required. This run has no Workspace, so publish an existing file through an explicit @chat/... or @temp/... path. Relative and @workspace paths return workspace_unavailable.")
		appendToolDefinitionNote(def, "This run has no Workspace. Artifact sources must use explicit @chat/... or @temp/... paths.")
	case "vision_recognize":
		setNestedToolParameterDescription(def.Parameters, []string{"properties", "images", "items", "properties", "file_path"},
			"Explicit host image path. This run has no Workspace, so relative paths and @workspace are unavailable. Prefer reference_name for a current Chat attachment or screenshot; otherwise use an explicit semantic-root or allowed absolute path.")
	}
}

func normalizedToolDefinitionName(def api.ToolDetailResponse) string {
	name := strings.ToLower(strings.TrimSpace(def.Name))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(def.Key))
	}
	return name
}

func requireToolParameter(schema map[string]any, name string) {
	if schema == nil || strings.TrimSpace(name) == "" {
		return
	}
	required := make([]any, 0)
	switch values := schema["required"].(type) {
	case []any:
		required = append(required, values...)
	case []string:
		for _, value := range values {
			required = append(required, value)
		}
	}
	for _, value := range required {
		if strings.EqualFold(strings.TrimSpace(valueAsString(value)), name) {
			schema["required"] = required
			return
		}
	}
	schema["required"] = append(required, name)
}

func valueAsString(value any) string {
	text, _ := value.(string)
	return text
}

func setToolParameterDescription(schema map[string]any, name string, description string) {
	properties, _ := schema["properties"].(map[string]any)
	property, _ := properties[name].(map[string]any)
	if property == nil {
		return
	}
	property["description"] = strings.TrimSpace(description)
}

func setNestedToolParameterDescription(schema map[string]any, path []string, description string) {
	current := schema
	for _, name := range path {
		next, _ := current[name].(map[string]any)
		if next == nil {
			return
		}
		current = next
	}
	current["description"] = strings.TrimSpace(description)
}

func appendToolDefinitionNote(def *api.ToolDetailResponse, note string) {
	note = strings.TrimSpace(note)
	if def == nil || note == "" || strings.Contains(def.Description, note) {
		return
	}
	def.Description = strings.TrimSpace(strings.Join([]string{def.Description, note}, "\n"))
}

func mergeToolDefinitions(base []api.ToolDetailResponse, local []api.ToolDetailResponse) []api.ToolDetailResponse {
	if len(local) == 0 {
		return append([]api.ToolDetailResponse(nil), base...)
	}
	out := make([]api.ToolDetailResponse, 0, len(base)+len(local))
	index := map[string]int{}
	appendDef := func(def api.ToolDetailResponse) {
		key := strings.ToLower(strings.TrimSpace(def.Name))
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(def.Key))
		}
		if key == "" {
			return
		}
		if pos, ok := index[key]; ok {
			out[pos] = def
			return
		}
		index[key] = len(out)
		out = append(out, def)
	}
	for _, def := range base {
		appendDef(def)
	}
	for _, def := range local {
		appendDef(def)
	}
	return out
}

func sandboxBashAsPublicBash(defs []api.ToolDetailResponse) (api.ToolDetailResponse, bool) {
	for _, def := range defs {
		if isToolDefinitionNamed(def, "bash_sandbox") || isToolDefinitionNamed(def, "_sandbox_bash_") {
			tool := cloneToolDefinition(def)
			tool.Key = "bash"
			tool.Name = "bash"
			return tool, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func isToolDefinitionNamed(def api.ToolDetailResponse, name string) bool {
	needle := strings.ToLower(strings.TrimSpace(name))
	return strings.EqualFold(strings.TrimSpace(def.Name), needle) || strings.EqualFold(strings.TrimSpace(def.Key), needle)
}
