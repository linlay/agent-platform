package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/observability"
)

type LLMAgentEngine struct {
	cfg        config.Config
	models     *ModelRegistry
	tools      ToolExecutor
	sandbox    SandboxClient
	httpClient *http.Client
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatRequest struct {
	Model         string           `json:"model"`
	Messages      []openAIMessage  `json:"messages"`
	Tools         []openAIToolSpec `json:"tools,omitempty"`
	ToolChoice    string           `json:"tool_choice,omitempty"`
	Temperature   float64          `json:"temperature,omitempty"`
	Stream        bool             `json:"stream,omitempty"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIToolSpec struct {
	Type     string               `json:"type"`
	Function openAIToolDefinition `json:"function"`
}

type openAIToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string                  `json:"content"`
			ReasoningContent string                  `json:"reasoning_content"`
			ReasoningDetails []map[string]any        `json:"reasoning_details"`
			ToolCalls        []openAIStreamToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type openAIStreamToolDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id"`
	Type     string                    `json:"type"`
	Function openAIStreamFunctionDelta `json:"function"`
}

type openAIStreamFunctionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type llmRunStream struct {
	engine              *LLMAgentEngine
	ctx                 context.Context
	req                 api.QueryRequest
	session             QuerySession
	runControl          *RunControl
	model               ModelDefinition
	provider            ProviderDefinition
	toolSpecs           []openAIToolSpec
	requestedToolNames  []string
	messages            []openAIMessage
	protocolConfig      protocolRuntimeConfig
	stageSettings       StageSettings
	execCtx             *ExecutionContext
	maxSteps            int
	toolChoice          string
	maxToolCallsPerTurn int
	postToolHook        func(string, string) PostToolHookResult

	step               int
	pending            []AgentDelta
	currentTurn        *providerTurnStream
	finished           bool
	closed             bool
	fallbackSent       bool
	cancelSent         bool
	finalTurnAttempted bool
	allowToolUse       bool
	previousToolResult any
	queuedToolCalls    []*preparedToolInvocation
	activeToolCall     *preparedToolInvocation
}

type providerTurnStream struct {
	body          io.ReadCloser
	reader        *bufio.Reader
	content       strings.Builder
	reasoning     strings.Builder
	thinkTag      thinkTagParserState
	toolCalls     map[int]*toolCallAccumulator
	finishReason  string
	hasMeaningful bool
}

type thinkTagParserState struct {
	buffer  strings.Builder
	inThink bool
}

type toolCallAccumulator struct {
	ID           string
	Type         string
	FunctionName string
	Arguments    strings.Builder
	EmittedBytes int
}

type preparedToolInvocation struct {
	toolID   string
	toolName string
	args     map[string]any
	prelude  []AgentDelta
}

// PostToolHookResult controls what happens after a tool call.
type PostToolHookResult int

const (
	PostToolContinue PostToolHookResult = iota // keep going
	PostToolStop                               // stop the stream (task status changed)
)

type runStreamOptions struct {
	ExecCtx             *ExecutionContext
	Messages            []openAIMessage
	ToolNames           []string
	ModelKey            string
	MaxSteps            int
	SystemPrompt        string
	Stage               string
	ToolChoice          string                                                  // "auto" (default), "required", "none"
	MaxToolCallsPerTurn int                                                     // 0 = unlimited; 1 = only first tool call per LLM turn (Java behaviour)
	PostToolHook        func(toolName string, toolID string) PostToolHookResult // called after each tool execution
}

func NewLLMAgentEngine(cfg config.Config, models *ModelRegistry, tools ToolExecutor, sandbox SandboxClient) *LLMAgentEngine {
	return NewLLMAgentEngineWithHTTPClient(cfg, models, tools, sandbox, nil)
}

func NewLLMAgentEngineWithHTTPClient(cfg config.Config, models *ModelRegistry, tools ToolExecutor, sandbox SandboxClient, httpClient *http.Client) *LLMAgentEngine {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &LLMAgentEngine{
		cfg:        cfg,
		models:     models,
		tools:      tools,
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
	if execCtx.RunControl == nil {
		execCtx.RunControl = RunControlFromContext(ctx)
	}
	if execCtx.Budget.RunTimeoutMs <= 0 {
		execCtx.Budget = normalizeBudget(session.ResolvedBudget)
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
		messages = []openAIMessage{
			{
				Role:    "system",
				Content: systemPrompt,
			},
		}
		// Append conversation history from previous runs
		for _, raw := range session.HistoryMessages {
			msg := rawMessageToOpenAI(raw)
			if msg.Role != "" {
				messages = append(messages, msg)
			}
		}
		// Append current user message
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
	protocolConfig := resolveProtocolRuntimeConfig(provider, model)
	stream := &llmRunStream{
		engine:              e,
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
		return 60 // Java default
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

func (s *llmRunStream) Next() (AgentDelta, error) {
	if len(s.pending) == 0 {
		if err := s.fillPending(); err != nil {
			return nil, err
		}
	}
	if len(s.pending) == 0 {
		return nil, io.EOF
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	return event, nil
}

func (s *llmRunStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.currentTurn != nil && s.currentTurn.body != nil {
		_ = s.currentTurn.body.Close()
		s.currentTurn = nil
	}
	s.engine.sandbox.CloseQuietly(s.execCtx)
	return nil
}

func (s *llmRunStream) prime() error {
	if len(s.pending) > 0 || s.finished {
		return nil
	}
	return s.fillPending()
}

func (s *llmRunStream) fillPending() error {
	for len(s.pending) == 0 {
		if err := s.handleInterruptIfNeeded(); err != nil || len(s.pending) > 0 {
			return err
		}
		if s.finished {
			return io.EOF
		}
		if s.activeToolCall != nil {
			toolName := s.activeToolCall.toolName
			toolID := s.activeToolCall.toolID
			if err := s.invokeActiveToolCall(); err != nil {
				return err
			}
			// Post-tool hook: allow caller to stop stream early (e.g. task status changed)
			if s.postToolHook != nil && s.postToolHook(toolName, toolID) == PostToolStop {
				s.queuedToolCalls = nil
				s.finished = true
			}
			continue
		}
		if len(s.queuedToolCalls) > 0 {
			s.activateNextToolCall()
			continue
		}
		if s.currentTurn == nil {
			if s.step >= s.maxSteps {
				// Java: force one final turn with ToolChoice.NONE to generate answer
				if !s.finalTurnAttempted {
					s.finalTurnAttempted = true
					s.toolSpecs = nil // disable tools for final turn
					if err := s.prepareNextTurn(); err != nil {
						return err
					}
					continue
				}
				s.enqueueFallback("Tool execution loop reached the maximum number of steps.")
				s.finished = true
				continue
			}
			if err := s.prepareNextTurn(); err != nil {
				return err
			}
			if len(s.pending) > 0 || s.currentTurn == nil {
				continue
			}
		}
		done, err := s.consumeCurrentTurn()
		if err != nil {
			return err
		}
		if done {
			continue
		}
	}
	return nil
}

func (s *llmRunStream) prepareNextTurn() error {
	s.appendPendingSteers()
	if len(s.pending) > 0 {
		return nil
	}
	// Emit react-step-N marker for each model turn (Java: ReactMode line 72)
	// Only for REACT mode (allowToolUse=true, not PLAN_EXECUTE which has its own markers)
	if s.allowToolUse && s.execCtx != nil && s.execCtx.PlanState == nil {
		s.pending = append(s.pending, DeltaStageMarker{
			Stage: fmt.Sprintf("react-step-%d", s.step+1),
		})
	}
	if err := s.checkBudgetBeforeModelCall(); err != nil {
		s.pending = append(s.pending, DeltaError{Error: err})
		s.finished = true
		return nil
	}
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateModelStreaming)
	}
	s.execCtx.RunLoopState = RunLoopStateModelStreaming
	if len(s.requestedToolNames) > 0 && len(s.toolSpecs) == 0 && !s.finalTurnAttempted {
		s.engine.logMissingToolSpecsWarning(s.session.RunID, s.requestedToolNames)
	}
	turn, err := s.engine.openProviderStream(s.ctx, s.session.RunID, s.provider, s.model, s.protocolConfig, s.stageSettings, s.messages, s.toolSpecs, s.toolChoice)
	if err != nil {
		return err
	}
	s.execCtx.ModelCalls++
	s.currentTurn = turn
	s.step++
	return nil
}

func (s *llmRunStream) consumeCurrentTurn() (bool, error) {
	eventName, rawChunk, err := readSSEFrame(s.currentTurn.reader)
	if err != nil {
		if s.isInterrupted() {
			return false, nil
		}
		if errors.Is(err, io.EOF) {
			if s.currentTurn.finishReason == "" && !s.currentTurn.hasMeaningful {
				return false, fmt.Errorf("provider stream ended before first valid event")
			}
			if s.currentTurn.finishReason == "" {
				return false, io.ErrUnexpectedEOF
			}
			return true, s.finishCurrentTurn()
		}
		return false, err
	}

	s.engine.logRawChunk(s.session.RunID, formatRawSSEFrame(eventName, rawChunk))
	if rawChunk == "" {
		return false, nil
	}
	if rawChunk == "[DONE]" {
		return true, s.finishCurrentTurn()
	}

	switch strings.ToUpper(strings.TrimSpace(s.model.Protocol)) {
	case "ANTHROPIC":
		return s.consumeAnthropicTurn(eventName, rawChunk)
	default:
		return s.consumeOpenAITurn(rawChunk)
	}
}

func (s *llmRunStream) consumeOpenAITurn(rawChunk string) (bool, error) {
	var decoded openAIStreamResponse
	if err := json.Unmarshal([]byte(rawChunk), &decoded); err != nil {
		return false, fmt.Errorf("decode provider stream chunk: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return false, fmt.Errorf("provider stream returned no choices")
	}

	for _, choice := range decoded.Choices {
		s.appendCompatReasoningFromOpenAI(choice.Delta.ReasoningContent, choice.Delta.ReasoningDetails)
		s.appendCompatContent(choice.Delta.Content)
		if len(choice.Delta.ToolCalls) > 0 {
			s.currentTurn.hasMeaningful = true
			for _, toolDelta := range choice.Delta.ToolCalls {
				deltas := s.currentTurn.appendOpenAIToolDelta(toolDelta)
				s.engine.logParsedToolDelta(s.session.RunID, toolDelta)
				if !s.allowToolUse {
					continue
				}
				s.pending = append(s.pending, deltas...)
			}
		}
		if strings.TrimSpace(choice.FinishReason) != "" {
			s.currentTurn.finishReason = strings.TrimSpace(choice.FinishReason)
			s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
			return true, s.finishCurrentTurn()
		}
	}

	return false, nil
}

func (s *llmRunStream) consumeAnthropicTurn(eventName string, rawChunk string) (bool, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawChunk), &payload); err != nil {
		return false, fmt.Errorf("decode provider stream chunk: %w", err)
	}

	switch strings.TrimSpace(eventName) {
	case "", "ping":
		return false, nil
	case "content_block_start":
		block := anyMapNode(payload["content_block"])
		blockType := anyStringNode(block["type"])
		if blockType == "tool_use" {
			index := anyIntNode(payload["index"])
			input, _ := block["input"].(map[string]any)
			toolID := anyStringNode(block["id"])
			toolName := anyStringNode(block["name"])
			var argsChunk string
			if len(input) > 0 {
				data, err := json.Marshal(input)
				if err != nil {
					return false, err
				}
				argsChunk = string(data)
			}
			deltas := s.currentTurn.appendToolCallDelta(index, toolID, "function", toolName, argsChunk)
			if !s.allowToolUse {
				return false, nil
			}
			s.pending = append(s.pending, deltas...)
		}
	case "content_block_delta":
		index := anyIntNode(payload["index"])
		delta := anyMapNode(payload["delta"])
		switch anyStringNode(delta["type"]) {
		case "text_delta":
			s.appendCompatContent(anyStringNode(delta["text"]))
		case "thinking_delta":
			s.appendCompatAnthropicThinking(anyStringNode(delta["thinking"]))
		case "input_json_delta":
			partialJSON := anyStringNode(delta["partial_json"])
			if partialJSON == "" {
				return false, nil
			}
			deltas := s.currentTurn.appendToolCallDelta(index, "", "function", "", partialJSON)
			if !s.allowToolUse {
				return false, nil
			}
			s.pending = append(s.pending, deltas...)
		case "signature_delta":
			return false, nil
		}
	case "message_delta":
		delta := anyMapNode(payload["delta"])
		stopReason := strings.TrimSpace(anyStringNode(delta["stop_reason"]))
		if stopReason == "" {
			return false, nil
		}
		if stopReason == "tool_use" {
			stopReason = "tool_calls"
		}
		s.currentTurn.finishReason = stopReason
		s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
		return true, s.finishCurrentTurn()
	case "message_stop":
		if strings.TrimSpace(s.currentTurn.finishReason) == "" {
			s.currentTurn.finishReason = "end_turn"
			s.engine.logParsedDelta(s.session.RunID, "finish_reason", s.currentTurn.finishReason)
		}
		return true, s.finishCurrentTurn()
	}

	return false, nil
}

func (s *llmRunStream) appendCompatReasoningFromOpenAI(reasoningContent string, reasoningDetails []map[string]any) {
	switch s.responseReasoningFormat() {
	case "REASONING_DETAILS_TEXT":
		for _, text := range extractReasoningDetailTexts(reasoningDetails) {
			s.appendReasoningDelta(text, "reasoning_details")
		}
	case "REASONING_CONTENT":
		s.appendReasoningDelta(reasoningContent, "reasoning_content")
	}
}

func (s *llmRunStream) appendCompatAnthropicThinking(thinking string) {
	if s.responseReasoningFormat() != "ANTHROPIC_THINKING_DELTA" {
		return
	}
	s.appendReasoningDelta(thinking, "thinking_delta")
}

func (s *llmRunStream) appendCompatContent(text string) {
	if text == "" {
		return
	}
	if s.responseReasoningFormat() == "THINK_TAG_CONTENT" {
		s.appendThinkTagContent(text, false)
		return
	}
	s.appendContentDelta(text)
}

func (s *llmRunStream) appendContentDelta(text string) {
	if text == "" {
		return
	}
	s.currentTurn.hasMeaningful = true
	s.currentTurn.content.WriteString(text)
	s.engine.logParsedDelta(s.session.RunID, "content", text)
	s.pending = append(s.pending, s.newContentDeltaEvent(text))
}

func (s *llmRunStream) appendReasoningDelta(text string, label string) {
	if text == "" {
		return
	}
	s.currentTurn.hasMeaningful = true
	s.currentTurn.reasoning.WriteString(text)
	s.engine.logParsedDelta(s.session.RunID, label, text)
	s.pending = append(s.pending, DeltaReasoning{Text: text})
}

func (s *llmRunStream) appendThinkTagContent(chunk string, flush bool) {
	const (
		startTag = "<think>"
		endTag   = "</think>"
	)

	s.currentTurn.hasMeaningful = true
	parser := &s.currentTurn.thinkTag
	parser.buffer.WriteString(chunk)
	for {
		pending := parser.buffer.String()
		if parser.inThink {
			index := strings.Index(pending, endTag)
			if index >= 0 {
				s.appendReasoningDelta(pending[:index], "think_tag")
				parser.buffer.Reset()
				parser.buffer.WriteString(pending[index+len(endTag):])
				parser.inThink = false
				continue
			}
			if !flush {
				flushLen := len(pending) - (len(endTag) - 1)
				if flushLen <= 0 {
					return
				}
				s.appendReasoningDelta(pending[:flushLen], "think_tag")
				parser.buffer.Reset()
				parser.buffer.WriteString(pending[flushLen:])
				return
			}
			s.appendReasoningDelta(pending, "think_tag")
			parser.buffer.Reset()
			return
		}

		index := strings.Index(pending, startTag)
		if index >= 0 {
			s.appendContentDelta(pending[:index])
			parser.buffer.Reset()
			parser.buffer.WriteString(pending[index+len(startTag):])
			parser.inThink = true
			continue
		}
		if !flush {
			flushLen := len(pending) - (len(startTag) - 1)
			if flushLen <= 0 {
				return
			}
			s.appendContentDelta(pending[:flushLen])
			parser.buffer.Reset()
			parser.buffer.WriteString(pending[flushLen:])
			return
		}
		s.appendContentDelta(pending)
		parser.buffer.Reset()
		return
	}
}

func (s *llmRunStream) responseReasoningFormat() string {
	format := anyStringNode(anyMapNode(s.protocolConfig.Compat["response"])["reasoningFormat"])
	if format == "" {
		switch strings.ToUpper(strings.TrimSpace(s.model.Protocol)) {
		case "ANTHROPIC":
			return "ANTHROPIC_THINKING_DELTA"
		default:
			return "REASONING_CONTENT"
		}
	}
	return strings.ToUpper(strings.TrimSpace(format))
}

func extractReasoningDetailTexts(details []map[string]any) []string {
	texts := make([]string, 0, len(details))
	for _, detail := range details {
		if text := anyStringNode(detail["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func (s *llmRunStream) finishCurrentTurn() error {
	if s.currentTurn != nil && s.responseReasoningFormat() == "THINK_TAG_CONTENT" {
		s.appendThinkTagContent("", true)
	}
	turn := s.currentTurn
	if turn == nil {
		return nil
	}
	s.currentTurn = nil
	if turn.body != nil {
		_ = turn.body.Close()
	}

	toolCalls, err := turn.materializeToolCalls()
	if err != nil {
		s.pending = append(s.pending, DeltaError{Error: NewErrorPayload(
			"missing_tool_call_id",
			err.Error(),
			ErrorScopeModel,
			ErrorCategoryModel,
			nil,
		)})
		s.finished = true
		return nil
	}
	// When maxToolCallsPerTurn is set, only keep the first N tool calls
	// in the assistant message (Java: only getFirst() from toolCalls).
	// This ensures the LLM only sees results for tool calls we actually execute.
	if s.maxToolCallsPerTurn > 0 && len(toolCalls) > s.maxToolCallsPerTurn {
		toolCalls = toolCalls[:s.maxToolCallsPerTurn]
	}

	content := turn.content.String()
	if content != "" || len(toolCalls) > 0 {
		msg := openAIMessage{Role: "assistant"}
		if content != "" {
			msg.Content = content
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		s.messages = append(s.messages, msg)
	}

	if len(toolCalls) == 0 {
		if strings.TrimSpace(content) == "" {
			s.enqueueFallback("Model returned no assistant content.")
		}
		if finishReason := strings.TrimSpace(turn.finishReason); finishReason != "" && !strings.EqualFold(finishReason, "tool_calls") {
			s.pending = append(s.pending, DeltaFinishReason{Reason: finishReason})
		}
		s.finished = true
		return nil
	}
	if !s.allowToolUse {
		s.pending = append(s.pending, DeltaError{Error: NewErrorPayload(
			"tool_calls_not_allowed",
			"tool calls are not allowed in ONESHOT mode",
			ErrorScopeRun,
			ErrorCategorySystem,
			nil,
		)})
		s.finished = true
		return nil
	}

	toolIDs := make([]string, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		toolIDs = append(toolIDs, toolCall.ID)
	}
	s.pending = append(s.pending, DeltaToolEnd{ToolIDs: toolIDs})
	for _, toolCall := range toolCalls {
		invocation, immediateEvents, toolMessage := s.prepareToolCall(toolCall)
		if len(immediateEvents) > 0 {
			s.pending = append(s.pending, immediateEvents...)
		}
		if toolMessage != nil {
			s.messages = append(s.messages, *toolMessage)
		}
		if invocation != nil {
			s.queuedToolCalls = append(s.queuedToolCalls, invocation)
		}
	}
	if s.activeToolCall == nil && len(s.queuedToolCalls) > 0 {
		s.activateNextToolCall()
	}
	return nil
}

func (s *llmRunStream) prepareToolCall(toolCall openAIToolCall) (*preparedToolInvocation, []AgentDelta, *openAIMessage) {
	toolID := toolCall.ID
	if strings.TrimSpace(toolID) == "" {
		return nil, []AgentDelta{DeltaError{Error: NewErrorPayload(
			"missing_tool_call_id",
			"provider tool call missing toolCallId",
			ErrorScopeModel,
			ErrorCategoryModel,
			nil,
		)}}, nil
	}

	args := map[string]any{}
	if strings.TrimSpace(toolCall.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			return nil, []AgentDelta{DeltaToolResult{
					ToolID:   toolID,
					ToolName: toolCall.Function.Name,
					Result: ToolExecutionResult{
						Output:   "invalid tool arguments: " + err.Error(),
						Error:    "invalid_tool_arguments",
						ExitCode: -1,
					},
				}}, &openAIMessage{
					Role:       "tool",
					ToolCallID: toolID,
					Name:       toolCall.Function.Name,
					Content:    "invalid tool arguments: " + err.Error(),
				}
		}
	}
	expandedArgs, err := ExpandToolArgsTemplates(args, s.previousToolResult)
	if err != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   err.Error(),
					Error:    "tool_args_template_missing_value",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    err.Error(),
			}
	}
	args, _ = expandedArgs.(map[string]any)

	return &preparedToolInvocation{
		toolID:   toolID,
		toolName: toolCall.Function.Name,
		args:     args,
		prelude:  s.preToolInvocationDeltas(toolID, toolCall.Function.Name, args),
	}, nil, nil
}

func (s *llmRunStream) activateNextToolCall() {
	if s.activeToolCall != nil || len(s.queuedToolCalls) == 0 {
		return
	}
	s.activeToolCall = s.queuedToolCalls[0]
	s.queuedToolCalls = s.queuedToolCalls[1:]
	if len(s.activeToolCall.prelude) > 0 {
		s.pending = append(s.pending, s.activeToolCall.prelude...)
	}
}

func (s *llmRunStream) invokeActiveToolCall() error {
	invocation := s.activeToolCall
	if invocation == nil {
		return nil
	}

	s.execCtx.CurrentToolID = invocation.toolID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}
	s.execCtx.ToolCalls++
	defer func() {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		s.activeToolCall = nil
	}()

	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, s.execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	s.previousToolResult = result.StructuredOrOutput()
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   invocation.toolID,
		ToolName: invocation.toolName,
		Result:   result,
	})
	// Emit plan.update after plan tool execution (mirrors Java PlanTaskDeltaBuilder).
	// Plan field must be the tasks array directly (not the full planStatePayload object),
	// because the frontend reads event.plan as List<PlanTask>.
	if isPlanTool(invocation.toolName) && s.execCtx != nil && s.execCtx.PlanState != nil && len(s.execCtx.PlanState.Tasks) > 0 {
		s.pending = append(s.pending, DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   planTasksArray(s.execCtx.PlanState),
		})
	}
	if published, ok := result.Structured["publishedArtifacts"].([]map[string]any); ok {
		for _, item := range published {
			s.pending = append(s.pending, DeltaArtifactPublish{
				ArtifactID: anyStringNode(item["artifactId"]),
				ChatID:     s.session.ChatID,
				RunID:      s.session.RunID,
				Artifact:   item,
			})
		}
	} else if published, ok := result.Structured["publishedArtifacts"].([]any); ok {
		for _, raw := range published {
			item, _ := raw.(map[string]any)
			if len(item) == 0 {
				continue
			}
			s.pending = append(s.pending, DeltaArtifactPublish{
				ArtifactID: anyStringNode(item["artifactId"]),
				ChatID:     s.session.ChatID,
				RunID:      s.session.RunID,
				Artifact:   item,
			})
		}
	}
	s.messages = append(s.messages, openAIMessage{
		Role:       "tool",
		ToolCallID: invocation.toolID,
		Name:       invocation.toolName,
		Content:    result.Output,
	})
	return nil
}

func (s *llmRunStream) checkBudgetBeforeModelCall() map[string]any {
	budget := normalizeBudget(s.execCtx.Budget)
	if budget.RunTimeoutMs > 0 && time.Since(s.execCtx.StartedAt) > budget.RunTimeout() {
		return NewErrorPayload(
			"run_timeout",
			"run exceeded configured timeout",
			ErrorScopeRun,
			ErrorCategoryTimeout,
			map[string]any{
				"elapsedMs": time.Since(s.execCtx.StartedAt).Milliseconds(),
				"timeoutMs": budget.RunTimeoutMs,
			},
		)
	}
	if budget.Model.MaxCalls > 0 && s.execCtx.ModelCalls > budget.Model.MaxCalls {
		return NewErrorPayload(
			"model_calls_exceeded",
			"model call budget exceeded",
			ErrorScopeModel,
			ErrorCategoryModel,
			map[string]any{
				"modelCalls": s.execCtx.ModelCalls,
				"limitValue": budget.Model.MaxCalls,
				"limitName":  "model.maxCalls",
			},
		)
	}
	if budget.Tool.MaxCalls > 0 && s.execCtx.ToolCalls > budget.Tool.MaxCalls {
		return NewErrorPayload(
			"tool_calls_exceeded",
			"tool call budget exceeded",
			ErrorScopeTool,
			ErrorCategoryTool,
			map[string]any{
				"toolCalls":  s.execCtx.ToolCalls,
				"limitValue": budget.Tool.MaxCalls,
				"limitName":  "tool.maxCalls",
			},
		)
	}
	return nil
}

func (s *llmRunStream) enqueueFallback(text string) {
	if s.fallbackSent {
		return
	}
	s.fallbackSent = true
	s.pending = append(s.pending, s.newContentDeltaEvent(text))
}

func (s *llmRunStream) newContentDeltaEvent(delta string) AgentDelta {
	return DeltaContent{Text: delta}
}

// AccumulatedMessages returns the messages accumulated during the stream's
// lifetime, including system prompt, user messages, assistant replies and
// tool results. Used by plan_execute to carry context into the summary stage.
func (s *llmRunStream) AccumulatedMessages() []openAIMessage {
	return append([]openAIMessage(nil), s.messages...)
}

func isPlanTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "_plan_add_tasks_", "_plan_update_task_":
		return true
	default:
		return false
	}
}

func (s *llmRunStream) appendPendingSteers() {
	if s.runControl == nil {
		return
	}
	for _, steer := range s.runControl.DrainSteers() {
		s.pending = append(s.pending, NewSteerDelta(steer))
		s.messages = append(s.messages, openAIMessage{
			Role:    "user",
			Content: steer.Message,
		})
	}
}

func (s *llmRunStream) isInterrupted() bool {
	return s.runControl != nil && s.runControl.Interrupted()
}

func (s *llmRunStream) handleInterruptIfNeeded() error {
	if !s.isInterrupted() {
		return nil
	}
	if s.currentTurn != nil && s.currentTurn.body != nil {
		_ = s.currentTurn.body.Close()
		s.currentTurn = nil
	}
	if !s.cancelSent {
		s.cancelSent = true
		s.pending = append(s.pending, DeltaRunCancel{RunID: s.session.RunID})
		return nil
	}
	return ErrRunInterrupted
}

func (s *llmRunStream) preToolInvocationDeltas(toolID string, toolName string, payload map[string]any) []AgentDelta {
	tool, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return nil
	}
	toolKind, _ := tool.Meta["kind"].(string)
	sourceType, _ := tool.Meta["sourceType"].(string)
	if strings.EqualFold(strings.TrimSpace(sourceType), "mcp") {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(toolKind), "frontend") {
		return nil
	}
	viewID, _ := tool.Meta["viewportKey"].(string)
	return []AgentDelta{NewFrontendSubmitRequest(s.session, toolID, payload, viewID)}
}

func (s *llmRunStream) lookupToolDefinition(toolName string) (api.ToolDetailResponse, bool) {
	for _, tool := range applyToolOverrides(s.engine.tools.Definitions(), s.execCtx.ToolOverrides) {
		if strings.EqualFold(strings.TrimSpace(tool.Name), strings.TrimSpace(toolName)) {
			return tool, true
		}
		if strings.EqualFold(strings.TrimSpace(tool.Key), strings.TrimSpace(toolName)) {
			return tool, true
		}
	}
	return api.ToolDetailResponse{}, false
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
		merged.Parameters = cloneAnyMap(override.Parameters)
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

func (t *providerTurnStream) appendOpenAIToolDelta(delta openAIStreamToolDelta) []AgentDelta {
	return t.appendToolCallDelta(delta.Index, delta.ID, delta.Type, delta.Function.Name, delta.Function.Arguments)
}

func (t *providerTurnStream) appendToolCallDelta(index int, toolID string, toolType string, toolName string, argumentsChunk string) []AgentDelta {
	if t.toolCalls == nil {
		t.toolCalls = map[int]*toolCallAccumulator{}
	}
	acc, ok := t.toolCalls[index]
	if !ok {
		acc = &toolCallAccumulator{}
		t.toolCalls[index] = acc
	}
	if toolID != "" {
		acc.ID = toolID
	}
	if toolType != "" {
		acc.Type = toolType
	}
	if toolName != "" {
		acc.FunctionName = toolName
	}
	if argumentsChunk != "" {
		acc.Arguments.WriteString(argumentsChunk)
	}
	if acc.ID == "" {
		return nil
	}
	arguments := acc.Arguments.String()
	if len(arguments) <= acc.EmittedBytes {
		return nil
	}
	argsDelta := arguments[acc.EmittedBytes:]
	acc.EmittedBytes = len(arguments)
	return []AgentDelta{DeltaToolCall{
		Index:     index,
		ID:        acc.ID,
		Name:      acc.FunctionName,
		ArgsDelta: argsDelta,
	}}
}

func (t *providerTurnStream) materializeToolCalls() ([]openAIToolCall, error) {
	if len(t.toolCalls) == 0 {
		return nil, nil
	}
	indexes := make([]int, 0, len(t.toolCalls))
	for idx := range t.toolCalls {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]openAIToolCall, 0, len(t.toolCalls))
	for _, idx := range indexes {
		acc := t.toolCalls[idx]
		if strings.TrimSpace(acc.ID) == "" {
			return nil, fmt.Errorf("provider tool call missing toolCallId for index %d", idx)
		}
		toolType := acc.Type
		if toolType == "" {
			toolType = "function"
		}
		out = append(out, openAIToolCall{
			ID:   acc.ID,
			Type: toolType,
			Function: openAIFunctionCall{
				Name:      acc.FunctionName,
				Arguments: acc.Arguments.String(),
			},
		})
	}
	return out, nil
}

func (e *LLMAgentEngine) openProviderStream(ctx context.Context, runID string, provider ProviderDefinition, model ModelDefinition, protocolConfig protocolRuntimeConfig, stageSettings StageSettings, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string) (*providerTurnStream, error) {
	if provider.BaseURL == "" {
		return nil, fmt.Errorf("provider %s has empty baseUrl", provider.Key)
	}
	if provider.APIKey == "" {
		return nil, fmt.Errorf("provider %s has empty apiKey", provider.Key)
	}
	protocol := strings.ToUpper(strings.TrimSpace(model.Protocol))
	if protocol == "" {
		protocol = "OPENAI"
	}
	endpoint := strings.TrimRight(provider.BaseURL, "/") + protocolConfig.EndpointPath

	switch protocol {
	case "OPENAI":
		return e.openOpenAIProviderStream(ctx, runID, provider, model, protocolConfig, stageSettings, messages, toolSpecs, toolChoice, endpoint)
	case "ANTHROPIC":
		return e.openAnthropicProviderStream(ctx, runID, provider, model, stageSettings, messages, toolSpecs, toolChoice, endpoint, protocolConfig)
	default:
		return nil, fmt.Errorf("streaming protocol %s is not supported", model.Protocol)
	}
}

type protocolRuntimeConfig struct {
	EndpointPath string
	Headers      map[string]string
	Compat       map[string]any
}

func resolveProtocolRuntimeConfig(provider ProviderDefinition, model ModelDefinition) protocolRuntimeConfig {
	protocol := strings.ToUpper(strings.TrimSpace(model.Protocol))
	if protocol == "" {
		protocol = "OPENAI"
	}
	def := provider.Protocol(protocol)
	endpointPath := def.EndpointPath
	if endpointPath == "" {
		endpointPath = defaultEndpointPath(protocol, provider.BaseURL)
	}
	return protocolRuntimeConfig{
		EndpointPath: endpointPath,
		Headers:      mergeStringMaps(defaultProtocolHeaders(protocol), def.Headers, model.Headers),
		Compat:       mergeAnyMaps(mergeAnyMaps(defaultProtocolCompat(protocol), def.Compat), model.Compat),
	}
}

func defaultProtocolHeaders(protocol string) map[string]string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		return map[string]string{
			"anthropic-version": "2023-06-01",
		}
	default:
		return nil
	}
}

func defaultProtocolCompat(protocol string) map[string]any {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		return map[string]any{
			"request": map[string]any{
				"whenReasoningEnabled": map[string]any{
					"thinking": map[string]any{},
				},
			},
			"response": map[string]any{
				"reasoningFormat": "ANTHROPIC_THINKING_DELTA",
			},
		}
	default:
		return map[string]any{
			"request": map[string]any{
				"whenReasoningEnabled": map[string]any{},
			},
			"response": map[string]any{
				"reasoningFormat": "REASONING_CONTENT",
			},
		}
	}
}

func mergeStringMaps(maps ...map[string]string) map[string]string {
	var out map[string]string
	for _, current := range maps {
		if len(current) == 0 {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		for key, value := range current {
			out[key] = value
		}
	}
	return out
}

func mergeAnyMaps(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := cloneAnyMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range overlay {
		if baseValue, ok := out[key].(map[string]any); ok {
			if overlayValue, ok := value.(map[string]any); ok {
				out[key] = mergeAnyMaps(baseValue, overlayValue)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func (e *LLMAgentEngine) openOpenAIProviderStream(ctx context.Context, runID string, provider ProviderDefinition, model ModelDefinition, protocolConfig protocolRuntimeConfig, stageSettings StageSettings, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, endpoint string) (*providerTurnStream, error) {
	effectiveToolChoice := "auto"
	if toolChoice != "" {
		effectiveToolChoice = toolChoice
	}
	if len(toolSpecs) == 0 {
		effectiveToolChoice = ""
	}
	requestBody := map[string]any{
		"model":          model.ModelID,
		"messages":       messages,
		"temperature":    0,
		"stream":         true,
		"stream_options": &streamOptions{IncludeUsage: true},
	}
	if len(toolSpecs) > 0 {
		requestBody["tools"] = toolSpecs
	}
	if effectiveToolChoice != "" {
		requestBody["tool_choice"] = effectiveToolChoice
	}
	if stageSettings.ReasoningEnabled {
		if compatRequest := anyMapNode(anyMapNode(protocolConfig.Compat["request"])["whenReasoningEnabled"]); compatRequest != nil {
			requestBody = mergeAnyMaps(requestBody, compatRequest)
		}
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	e.logOutgoingRequest(runID, provider, model, endpoint, messages, toolSpecs, effectiveToolChoice, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	for key, value := range protocolConfig.Headers {
		req.Header.Set(key, value)
	}

	return e.executeProviderRequest(req)
}

func (e *LLMAgentEngine) openAnthropicProviderStream(ctx context.Context, runID string, provider ProviderDefinition, model ModelDefinition, stageSettings StageSettings, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, endpoint string, protocolConfig protocolRuntimeConfig) (*providerTurnStream, error) {
	requestBody, effectiveToolChoice, err := e.buildAnthropicRequestBody(model, stageSettings, messages, toolSpecs, toolChoice, protocolConfig)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	e.logOutgoingRequest(runID, provider, model, endpoint, messages, toolSpecs, effectiveToolChoice, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Api-Key", provider.APIKey)
	for key, value := range protocolConfig.Headers {
		req.Header.Set(key, value)
	}

	return e.executeProviderRequest(req)
}

func (e *LLMAgentEngine) executeProviderRequest(req *http.Request) (*providerTurnStream, error) {
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("model request failed with status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("model request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return &providerTurnStream{
		body:   resp.Body,
		reader: bufio.NewReader(resp.Body),
	}, nil
}

func (e *LLMAgentEngine) buildAnthropicRequestBody(model ModelDefinition, stageSettings StageSettings, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, protocolConfig protocolRuntimeConfig) (map[string]any, string, error) {
	systemPrompt, anthropicMessages, err := convertMessagesToAnthropic(messages)
	if err != nil {
		return nil, "", err
	}

	effectiveToolChoice := strings.TrimSpace(strings.ToLower(toolChoice))
	if effectiveToolChoice == "" {
		effectiveToolChoice = "auto"
	}
	if len(toolSpecs) == 0 || effectiveToolChoice == "none" {
		effectiveToolChoice = ""
	}

	requestBody := map[string]any{
		"model":      model.ModelID,
		"messages":   anthropicMessages,
		"stream":     true,
		"max_tokens": resolveAnthropicMaxTokens(e.cfg, stageSettings),
	}
	if strings.TrimSpace(systemPrompt) != "" {
		requestBody["system"] = systemPrompt
	}
	if len(toolSpecs) > 0 && effectiveToolChoice != "" {
		requestBody["tools"] = toAnthropicToolSpecs(toolSpecs)
		requestBody["tool_choice"] = anthropicToolChoice(effectiveToolChoice, stageSettings.ReasoningEnabled)
	}

	if stageSettings.ReasoningEnabled {
		requestBody["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": reasoningBudgetTokens(stageSettings.ReasoningEffort),
		}
		if compatRequest := anyMapNode(anyMapNode(protocolConfig.Compat["request"])["whenReasoningEnabled"]); len(compatRequest) > 0 {
			requestBody = mergeAnyMaps(requestBody, compatRequest)
		}
	}

	return requestBody, effectiveToolChoice, nil
}

func convertMessagesToAnthropic(messages []openAIMessage) (string, []map[string]any, error) {
	var systemParts []string
	var out []map[string]any
	for index := 0; index < len(messages); index++ {
		msg := messages[index]
		switch strings.TrimSpace(msg.Role) {
		case "system":
			if text := anthropicTextFromContent(msg.Content); text != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			blocks := anthropicTextBlocks(msg.Content)
			if len(blocks) > 0 {
				out = append(out, map[string]any{"role": "user", "content": blocks})
			}
		case "assistant":
			blocks, err := anthropicAssistantBlocks(msg)
			if err != nil {
				return "", nil, err
			}
			if len(blocks) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": blocks})
			}
		case "tool":
			blocks := make([]map[string]any, 0, 2)
			for index < len(messages) && strings.TrimSpace(messages[index].Role) == "tool" {
				toolMsg := messages[index]
				blocks = append(blocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": toolMsg.ToolCallID,
					"content":     anthropicTextFromContent(toolMsg.Content),
				})
				index++
			}
			if index < len(messages) && strings.TrimSpace(messages[index].Role) == "user" {
				blocks = append(blocks, anthropicTextBlocks(messages[index].Content)...)
			} else {
				index--
			}
			if len(blocks) > 0 {
				out = append(out, map[string]any{"role": "user", "content": blocks})
			}
		}
	}
	return strings.Join(systemParts, "\n\n"), out, nil
}

func anthropicAssistantBlocks(msg openAIMessage) ([]map[string]any, error) {
	blocks := anthropicTextBlocks(msg.Content)
	for _, toolCall := range msg.ToolCalls {
		input := map[string]any{}
		if strings.TrimSpace(toolCall.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
				return nil, fmt.Errorf("decode assistant tool call %s: %w", toolCall.ID, err)
			}
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    toolCall.ID,
			"name":  toolCall.Function.Name,
			"input": input,
		})
	}
	return blocks, nil
}

func anthropicTextBlocks(content any) []map[string]any {
	text := anthropicTextFromContent(content)
	if text == "" {
		return nil
	}
	return []map[string]any{{"type": "text", "text": text}}
}

func anthropicTextFromContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func toAnthropicToolSpecs(specs []openAIToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		out = append(out, map[string]any{
			"name":         spec.Function.Name,
			"description":  spec.Function.Description,
			"input_schema": spec.Function.Parameters,
		})
	}
	return out
}

func anthropicToolChoice(toolChoice string, reasoningEnabled bool) map[string]any {
	switch strings.TrimSpace(strings.ToLower(toolChoice)) {
	case "required":
		if reasoningEnabled {
			return map[string]any{"type": "auto"}
		}
		return map[string]any{"type": "any"}
	case "auto":
		return map[string]any{"type": "auto"}
	default:
		return map[string]any{"type": "auto"}
	}
}

func resolveAnthropicMaxTokens(cfg config.Config, stageSettings StageSettings) int {
	if stageSettings.MaxTokens > 0 {
		return stageSettings.MaxTokens
	}
	if cfg.Defaults.MaxTokens > 0 {
		return cfg.Defaults.MaxTokens
	}
	return 4096
}

func reasoningBudgetTokens(effort string) int {
	switch strings.ToUpper(strings.TrimSpace(effort)) {
	case "LOW":
		return 1024
	case "HIGH":
		return 4096
	case "MEDIUM":
		fallthrough
	default:
		return 2048
	}
}

func (e *LLMAgentEngine) logOutgoingRequest(runID string, provider ProviderDefinition, model ModelDefinition, endpoint string, messages []openAIMessage, toolSpecs []openAIToolSpec, toolChoice string, body []byte) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	if strings.TrimSpace(toolChoice) == "" {
		toolChoice = "none"
	}
	log.Printf(
		"[llm][run:%s][request_summary] provider=%s endpoint=%s model=%s messageCount=%d toolCount=%d toolChoice=%s",
		runID,
		e.formatLogText(provider.Key),
		e.formatLogText(endpoint),
		e.formatLogText(model.ModelID),
		len(messages),
		len(toolSpecs),
		e.formatLogText(toolChoice),
	)
	log.Printf(
		"[llm][run:%s][request_body] provider=%s endpoint=%s model=%s body=%s",
		runID,
		e.formatLogText(provider.Key),
		e.formatLogText(endpoint),
		e.formatLogText(model.ModelID),
		e.formatLogText(string(body)),
	)
}

func (e *LLMAgentEngine) logMissingToolSpecsWarning(runID string, requestedToolNames []string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf(
		"[llm][run:%s][warning] requestedTools=%s no tool schema included in provider request",
		runID,
		e.formatLogText(strings.Join(requestedToolNames, ",")),
	)
}

func (e *LLMAgentEngine) logRawChunk(runID string, chunk string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf("[llm][run:%s][raw_chunk] %s", runID, e.formatLogText(chunk))
}

func (e *LLMAgentEngine) logParsedDelta(runID string, kind string, value string) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf("[llm][run:%s][parsed_%s] %s", runID, kind, e.formatLogText(value))
}

func (e *LLMAgentEngine) logParsedToolDelta(runID string, delta openAIStreamToolDelta) {
	if !e.cfg.Logging.LLMInteraction.Enabled {
		return
	}
	log.Printf(
		"[llm][run:%s][parsed_tool_call] index=%d id=%s type=%s name=%s args=%s",
		runID,
		delta.Index,
		e.formatLogText(delta.ID),
		e.formatLogText(delta.Type),
		e.formatLogText(delta.Function.Name),
		e.formatLogText(delta.Function.Arguments),
	)
}

func (e *LLMAgentEngine) formatLogText(text string) string {
	normalized := observability.SanitizeLog(strings.ReplaceAll(strings.TrimSpace(text), "\n", "\\n"))
	if normalized == "" {
		return `""`
	}
	if e.cfg.Logging.LLMInteraction.MaskSensitive {
		return fmt.Sprintf("[masked chars=%d]", len(normalized))
	}
	return normalized
}

func readSSEFrame(reader *bufio.Reader) (string, string, error) {
	var eventName string
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 || eventName != "" {
				return eventName, strings.Join(dataLines, "\n"), nil
			}
			if errors.Is(err, io.EOF) {
				return "", "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			if errors.Is(err, io.EOF) {
				return "", "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 && eventName == "" {
				return "", "", io.EOF
			}
			return eventName, strings.Join(dataLines, "\n"), nil
		}
	}
}

func formatRawSSEFrame(eventName string, rawChunk string) string {
	if strings.TrimSpace(eventName) == "" {
		return rawChunk
	}
	return "event: " + strings.TrimSpace(eventName) + "\ndata: " + rawChunk
}

func (r ToolExecutionResult) StructuredOrOutput() any {
	if len(r.Structured) > 0 {
		return r.Structured
	}
	return r.Output
}

func toOpenAIToolSpecs(defs []api.ToolDetailResponse) []openAIToolSpec {
	out := make([]openAIToolSpec, 0, len(defs))
	for _, def := range defs {
		out = append(out, openAIToolSpec{
			Type: "function",
			Function: openAIToolDefinition{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}
	return out
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

// rawMessageToOpenAI converts a raw_messages.jsonl entry to an openAIMessage.
// Format follows the Java version: role + content, with tool_calls for assistant messages.
func rawMessageToOpenAI(raw map[string]any) openAIMessage {
	role, _ := raw["role"].(string)
	content, _ := raw["content"].(string)
	if role == "" {
		return openAIMessage{}
	}
	msg := openAIMessage{Role: role, Content: content}
	if role == "assistant" {
		if calls, ok := raw["tool_calls"].([]any); ok {
			for _, c := range calls {
				callMap, _ := c.(map[string]any)
				if callMap == nil {
					continue
				}
				tc := openAIToolCall{}
				tc.ID, _ = callMap["id"].(string)
				tc.Type, _ = callMap["type"].(string)
				if tc.Type == "" {
					tc.Type = "function"
				}
				if fn, ok := callMap["function"].(map[string]any); ok {
					tc.Function.Name, _ = fn["name"].(string)
					tc.Function.Arguments, _ = fn["arguments"].(string)
				}
				msg.ToolCalls = append(msg.ToolCalls, tc)
			}
			// Assistant messages with tool_calls must not have content per OpenAI spec
			if len(msg.ToolCalls) > 0 && content == "" {
				msg.Content = nil
			}
		}
	}
	if role == "tool" {
		msg.ToolCallID, _ = raw["tool_call_id"].(string)
		msg.Name, _ = raw["name"].(string)
	}
	return msg
}
