package llm

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/hitl"
	. "agent-platform-runner-go/internal/models"
)

type LLMAgentEngine struct {
	cfg        config.Config
	models     *ModelRegistry
	tools      ToolExecutor
	frontend   *frontendtools.Registry
	sandbox    SandboxClient
	httpClient *http.Client
	hitl       *hitl.Registry
}

type runStreamOptions struct {
	ExecCtx             *ExecutionContext
	Messages            []openAIMessage
	ToolNames           []string
	ModelKey            string
	MaxSteps            int
	SystemPrompt        string
	Stage               string
	ToolChoice          string
	MaxToolCallsPerTurn int
	PostToolHook        func(toolName string, toolID string) PostToolHookResult
}

func NewLLMAgentEngine(cfg config.Config, models *ModelRegistry, tools ToolExecutor, frontend *frontendtools.Registry, sandbox SandboxClient, hitlRegistry *hitl.Registry) *LLMAgentEngine {
	return NewLLMAgentEngineWithHTTPClient(cfg, models, tools, frontend, sandbox, hitlRegistry, nil)
}

func NewLLMAgentEngineWithHTTPClient(cfg config.Config, models *ModelRegistry, tools ToolExecutor, frontend *frontendtools.Registry, sandbox SandboxClient, hitlRegistry *hitl.Registry, httpClient *http.Client) *LLMAgentEngine {
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
		hitl:       hitlRegistry,
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
	model, provider, err := e.models.Get(modelKey)
	if err != nil {
		return nil, err
	}
	allowedTools := session.ToolNames
	if options.ToolNames != nil {
		allowedTools = options.ToolNames
	}
	effectiveDefs := applyToolOverrides(filterToolDefinitions(e.tools.Definitions(), allowedTools), session.ToolOverrides)
	toolSpecs := toOpenAIToolSpecs(effectiveDefs)
	execCtx := options.ExecCtx
	if execCtx == nil {
		execCtx = &ExecutionContext{
			Request:       req,
			Session:       session,
			Budget:        session.ResolvedBudget,
			StageSettings: session.ResolvedStageSettings,
			ToolOverrides: cloneToolOverrides(session.ToolOverrides),
			RunLoopState:  RunLoopStateIdle,
		}
	}
	execCtx.Request = req
	execCtx.Session = session
	execCtx.HITLLevel = AnyIntNode(req.Params["hitlLevel"])
	if execCtx.RunControl == nil {
		execCtx.RunControl = RunControlFromContext(ctx)
	}
	if execCtx.Budget.RunTimeoutMs <= 0 {
		execCtx.Budget = NormalizeBudget(session.ResolvedBudget)
	}
	if execCtx.StartedAt.IsZero() {
		execCtx.StartedAt = time.Now()
	}
	if execCtx.RunControl != nil {
		execCtx.RunControl.TransitionState(RunLoopStateModelStreaming)
	}
	messages := options.Messages
	if len(messages) == 0 {
		systemPrompt := buildSystemPrompt(session, req, model.Key, PromptBuildOptions{
			Stage:                   options.Stage,
			StageInstructionsPrompt: "",
			StageSystemPrompt:       "",
			ToolDefinitions:         effectiveDefs,
			IncludeAfterCallHints:   true,
		})
		log.Printf("[llm][run:%s][%s] LLM delta stream system prompt:\n%s", session.RunID, options.Stage, systemPrompt)
		messages = []openAIMessage{{
			Role:    "system",
			Content: systemPrompt,
		}}
		for _, raw := range mergeRawMessagesByMsgID(session.HistoryMessages) {
			msg := rawMessageToOpenAI(raw)
			if msg.Role != "" {
				messages = append(messages, msg)
			}
		}
		messages = append(messages, openAIMessage{
			Role:    "user",
			Content: req.Message,
		})
	}
	maxSteps := options.MaxSteps
	if maxSteps <= 0 {
		maxSteps = e.resolveMaxSteps()
	}

	toolChoice := strings.TrimSpace(strings.ToLower(options.ToolChoice))
	if toolChoice == "" {
		toolChoice = "auto"
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
		protocolConfig:      resolveProtocolRuntimeConfig(provider, model),
		stageSettings:       stageSettingsForName(session.ResolvedStageSettings, options.Stage),
		execCtx:             execCtx,
		maxSteps:            maxSteps,
		toolChoice:          toolChoice,
		maxToolCallsPerTurn: options.MaxToolCallsPerTurn,
		postToolHook:        options.PostToolHook,
		allowToolUse:        allowToolUse,
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

func (e *LLMAgentEngine) resolveMaxSteps() int {
	maxSteps := e.cfg.Defaults.React.MaxSteps
	if maxSteps <= 0 {
		return 60
	}
	return maxSteps
}

func stageSettingsForName(settings PlanExecuteSettings, stage string) StageSettings {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "plan":
		return settings.Plan
	case "summary":
		return settings.Summary
	default:
		return settings.Execute
	}
}

func filterToolDefinitions(defs []api.ToolDetailResponse, allowed []string) []api.ToolDetailResponse {
	if len(allowed) == 0 {
		return defs
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

func applyToolOverrides(defs []api.ToolDetailResponse, overrides map[string]api.ToolDetailResponse) []api.ToolDetailResponse {
	if len(overrides) == 0 {
		return defs
	}
	out := make([]api.ToolDetailResponse, 0, len(defs))
	for _, def := range defs {
		override, ok := overrides[normalizeOverrideKey(def.Name)]
		if !ok {
			override, ok = overrides[normalizeOverrideKey(def.Key)]
		}
		if !ok {
			out = append(out, def)
			continue
		}
		out = append(out, mergeToolOverride(def, override))
	}
	return out
}

func mergeToolOverride(base api.ToolDetailResponse, override api.ToolDetailResponse) api.ToolDetailResponse {
	merged := cloneToolDefinition(base)
	if strings.TrimSpace(override.Key) != "" {
		merged.Key = override.Key
	}
	if strings.TrimSpace(override.Name) != "" {
		merged.Name = override.Name
	}
	if strings.TrimSpace(override.Label) != "" {
		merged.Label = override.Label
	}
	if strings.TrimSpace(override.Description) != "" {
		merged.Description = override.Description
	}
	if strings.TrimSpace(override.AfterCallHint) != "" {
		merged.AfterCallHint = override.AfterCallHint
	}
	if len(override.Parameters) > 0 {
		merged.Parameters = CloneMap(override.Parameters)
	}
	if len(merged.Meta) == 0 {
		merged.Meta = map[string]any{}
	}
	for key, value := range override.Meta {
		merged.Meta[key] = value
	}
	return merged
}

func cloneToolOverrides(src map[string]api.ToolDetailResponse) map[string]api.ToolDetailResponse {
	if src == nil {
		return nil
	}
	out := make(map[string]api.ToolDetailResponse, len(src))
	for key, value := range src {
		out[key] = cloneToolDefinition(value)
	}
	return out
}

func normalizeOverrideKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
