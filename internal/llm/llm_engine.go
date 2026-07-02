package llm

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/hitl"
	. "agent-platform/internal/models"
	"agent-platform/internal/querymessages"
)

type LLMAgentEngine struct {
	cfg        config.Config
	models     *ModelRegistry
	tools      ToolExecutor
	frontend   *frontendtools.Registry
	sandbox    SandboxClient
	httpClient *http.Client
}

type runStreamOptions struct {
	ExecCtx                      *ExecutionContext
	Messages                     []openAIMessage
	ToolNames                    []string
	ModelKey                     string
	MaxSteps                     int
	Stage                        string
	ToolChoice                   string
	PreserveProvidedSystemPrompt bool
	PostToolHook                 func(toolName string, toolID string) PostToolHookResult
}

func NewLLMAgentEngine(cfg config.Config, models *ModelRegistry, tools ToolExecutor, frontend *frontendtools.Registry, sandbox SandboxClient) *LLMAgentEngine {
	return NewLLMAgentEngineWithHTTPClient(cfg, models, tools, frontend, sandbox, nil)
}

func NewLLMAgentEngineWithHTTPClient(cfg config.Config, models *ModelRegistry, tools ToolExecutor, frontend *frontendtools.Registry, sandbox SandboxClient, httpClient *http.Client) *LLMAgentEngine {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &LLMAgentEngine{
		cfg:        cfg,
		models:     models,
		tools:      tools,
		frontend:   frontend,
		sandbox:    sandbox,
		httpClient: httpClient,
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
	stageSettings := stageSettingsForName(session.ResolvedStageSettings, options.Stage)
	budgetStage := budgetStageForName(session, options.Stage)
	allowedTools := session.ToolNames
	if options.ToolNames != nil {
		allowedTools = options.ToolNames
	}
	allowedTools = coderRuntimeToolNamesForStage(session, options.Stage, allowedTools)
	effectiveDefs := effectiveToolDefinitions(e.tools.Definitions(), allowedTools, session.AgentHasRuntimeSandbox)
	toolSpecs := toOpenAIToolSpecs(effectiveDefs)
	execCtx := options.ExecCtx
	if execCtx == nil {
		execCtx = &ExecutionContext{
			Request:       req,
			Session:       session,
			Budget:        session.ResolvedBudget,
			StageSettings: session.ResolvedStageSettings,
			AccessLevel:   session.AccessLevel,
			RunLoopState:  RunLoopStateIdle,
		}
	}
	execCtx.Request = req
	execCtx.Session = session
	execCtx.AccessLevel = session.AccessLevel
	if len(execCtx.RuntimeEnvOverrides) == 0 {
		execCtx.RuntimeEnvOverrides = CloneStringMap(session.RuntimeEnvOverrides)
	}
	if execCtx.RunControl == nil {
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
	useCachedSystemInit := cacheOK && !(len(options.Messages) > 0 && options.PreserveProvidedSystemPrompt)
	if useCachedSystemInit {
		toolSpecs = cachedTools
	}
	if execCtx.RunControl != nil {
		execCtx.RunControl.TransitionState(RunLoopStateModelStreaming)
	}
	messages := options.Messages
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
		for _, raw := range currentMessages {
			msg := rawMessageToOpenAI(raw, preserveReasoning)
			if msg.Role != "" {
				messages = append(messages, msg)
			}
		}
	} else if useCachedSystemInit {
		messages = replaceSystemMessage(messages, cachedSystem)
	}
	maxSteps := options.MaxSteps
	if stageMaxSteps := budgetStageMaxSteps(session.ResolvedBudget, budgetStage); stageMaxSteps > 0 {
		maxSteps = stageMaxSteps
	} else if maxSteps <= 0 {
		maxSteps = e.resolveMaxSteps(session, budgetStage)
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
		engine:              e,
		protocol:            resolveProtocol(e, model),
		ctx:                 ctx,
		req:                 req,
		session:             session,
		runControl:          execCtx.RunControl,
		model:               model,
		provider:            provider,
		toolSpecs:           toolSpecs,
		requestedToolNames:  append([]string(nil), allowedTools...),
		messages:            append([]openAIMessage(nil), messages...),
		protocolConfig:      protocolConfig,
		stageSettings:       stageSettings,
		execCtx:             execCtx,
		maxSteps:            maxSteps,
		budgetStage:         budgetStage,
		toolChoice:          toolChoice,
		postToolHook:        options.PostToolHook,
		allowToolUse:        allowToolUse,
		promptBuildOptions:  promptBuildOptions,
		onApprovalSummary:   approvalSummarySinkFromContext(ctx),
		systemInitCacheKey:  cacheKey,
		systemInitCacheUsed: useCachedSystemInit,
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
	if strings.Contains(normalized, "plan") {
		return "plan"
	}
	if strings.Contains(normalized, "execute") || normalized == "coder" {
		return "execute"
	}
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(session.Mode))
	}
	switch normalized {
	case "coder":
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

func stageSettingsForName(settings PlanExecuteSettings, stage string) StageSettings {
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
		filtered := make([]api.ToolDetailResponse, 0, len(defs))
		for _, def := range defs {
			if explicitOnly, _ := def.Meta["explicitOnly"].(bool); explicitOnly {
				continue
			}
			filtered = append(filtered, def)
		}
		return filtered
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

func effectiveToolDefinitions(defs []api.ToolDetailResponse, allowed []string, useSandboxBash bool) []api.ToolDetailResponse {
	filtered := filterToolDefinitions(defs, allowed)
	if !useSandboxBash {
		return filtered
	}
	sandboxBash, ok := sandboxBashAsPublicBash(defs)
	if !ok {
		return filtered
	}
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
