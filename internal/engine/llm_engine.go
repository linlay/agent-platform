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
	Model       string           `json:"model"`
	Messages    []openAIMessage  `json:"messages"`
	Tools       []openAIToolSpec `json:"tools,omitempty"`
	ToolChoice  string           `json:"tool_choice,omitempty"`
	Temperature float64          `json:"temperature,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
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
	engine     *LLMAgentEngine
	ctx        context.Context
	req        api.QueryRequest
	session    QuerySession
	runControl *RunControl
	model      ModelDefinition
	provider   ProviderDefinition
	toolSpecs  []openAIToolSpec
	messages   []openAIMessage
	execCtx    *ExecutionContext
	maxSteps   int

	step               int
	pending            []AgentDelta
	currentTurn        *providerTurnStream
	finished           bool
	closed             bool
	fallbackSent       bool
	cancelSent         bool
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
	toolCalls     map[int]*toolCallAccumulator
	finishReason  string
	hasMeaningful bool
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

type runStreamOptions struct {
	ExecCtx      *ExecutionContext
	Messages     []openAIMessage
	ToolNames    []string
	ModelKey     string
	MaxSteps     int
	SystemPrompt string
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
	return e.newRunStreamWithOptions(ctx, req, session, allowToolUse, runStreamOptions{})
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
		systemPrompt := buildSystemPrompt(session, req, model.Key)
		if strings.TrimSpace(options.SystemPrompt) != "" {
			systemPrompt = strings.TrimSpace(options.SystemPrompt)
		}
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

	stream := &llmRunStream{
		engine:       e,
		ctx:          ctx,
		req:          req,
		session:      session,
		runControl:   execCtx.RunControl,
		model:        model,
		provider:     provider,
		toolSpecs:    toolSpecs,
		messages:     append([]openAIMessage(nil), messages...),
		execCtx:      execCtx,
		maxSteps:     maxSteps,
		allowToolUse: allowToolUse,
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
		return 6
	}
	return maxSteps
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
			if err := s.invokeActiveToolCall(); err != nil {
				return err
			}
			continue
		}
		if len(s.queuedToolCalls) > 0 {
			s.activateNextToolCall()
			continue
		}
		if s.currentTurn == nil {
			if s.step >= s.maxSteps {
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
	if err := s.checkBudgetBeforeModelCall(); err != nil {
		s.pending = append(s.pending, DeltaError{Error: err})
		s.finished = true
		return nil
	}
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateModelStreaming)
	}
	s.execCtx.RunLoopState = RunLoopStateModelStreaming
	turn, err := s.engine.openProviderStream(s.ctx, s.provider, s.model, s.messages, s.toolSpecs)
	if err != nil {
		return err
	}
	s.execCtx.ModelCalls++
	s.currentTurn = turn
	s.step++
	return nil
}

func (s *llmRunStream) consumeCurrentTurn() (bool, error) {
	rawChunk, err := readSSEData(s.currentTurn.reader)
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

	s.engine.logRawChunk(s.session.RunID, rawChunk)
	if rawChunk == "" {
		return false, nil
	}
	if rawChunk == "[DONE]" {
		return true, s.finishCurrentTurn()
	}

	var decoded openAIStreamResponse
	if err := json.Unmarshal([]byte(rawChunk), &decoded); err != nil {
		return false, fmt.Errorf("decode provider stream chunk: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return false, fmt.Errorf("provider stream returned no choices")
	}

	for _, choice := range decoded.Choices {
		if choice.Delta.Content != "" {
			s.currentTurn.hasMeaningful = true
			s.currentTurn.content.WriteString(choice.Delta.Content)
			s.engine.logParsedDelta(s.session.RunID, "content", choice.Delta.Content)
			s.pending = append(s.pending, s.newContentDeltaEvent(choice.Delta.Content))
		}
		if choice.Delta.ReasoningContent != "" {
			s.currentTurn.hasMeaningful = true
			s.currentTurn.reasoning.WriteString(choice.Delta.ReasoningContent)
			s.engine.logParsedDelta(s.session.RunID, "reasoning_content", choice.Delta.ReasoningContent)
			s.pending = append(s.pending, DeltaReasoning{Text: choice.Delta.ReasoningContent})
		}
		if len(choice.Delta.ToolCalls) > 0 {
			s.currentTurn.hasMeaningful = true
			for _, toolDelta := range choice.Delta.ToolCalls {
				deltas := s.currentTurn.appendToolDelta(toolDelta)
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

func (s *llmRunStream) finishCurrentTurn() error {
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
	if budget.Model.MaxCalls > 0 && s.execCtx.ModelCalls >= budget.Model.MaxCalls {
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

func (t *providerTurnStream) appendToolDelta(delta openAIStreamToolDelta) []AgentDelta {
	if t.toolCalls == nil {
		t.toolCalls = map[int]*toolCallAccumulator{}
	}
	acc, ok := t.toolCalls[delta.Index]
	if !ok {
		acc = &toolCallAccumulator{}
		t.toolCalls[delta.Index] = acc
	}
	if delta.ID != "" {
		acc.ID = delta.ID
	}
	if delta.Type != "" {
		acc.Type = delta.Type
	}
	if delta.Function.Name != "" {
		acc.FunctionName = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		acc.Arguments.WriteString(delta.Function.Arguments)
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
		Index:     delta.Index,
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

func (e *LLMAgentEngine) openProviderStream(ctx context.Context, provider ProviderDefinition, model ModelDefinition, messages []openAIMessage, toolSpecs []openAIToolSpec) (*providerTurnStream, error) {
	if provider.BaseURL == "" {
		return nil, fmt.Errorf("provider %s has empty baseUrl", provider.Key)
	}
	if provider.APIKey == "" {
		return nil, fmt.Errorf("provider %s has empty apiKey", provider.Key)
	}
	if model.Protocol != "" && model.Protocol != "OPENAI" {
		return nil, fmt.Errorf("streaming protocol %s is not supported", model.Protocol)
	}

	requestBody := openAIChatRequest{
		Model:       model.ModelID,
		Messages:    messages,
		Tools:       toolSpecs,
		ToolChoice:  "auto",
		Temperature: 0,
		Stream:      true,
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(provider.BaseURL, "/") + provider.EndpointPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

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

func readSSEData(reader *bufio.Reader) (string, error) {
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 {
				return strings.Join(dataLines, "\n"), nil
			}
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 {
				return "", io.EOF
			}
			return strings.Join(dataLines, "\n"), nil
		}
	}
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
