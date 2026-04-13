package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
	. "agent-platform-runner-go/internal/models"
)

type llmRunStream struct {
	engine              *LLMAgentEngine
	protocol            providerProtocol
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
	PostToolContinue PostToolHookResult = iota
	PostToolStop
)

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
				if !s.finalTurnAttempted {
					s.finalTurnAttempted = true
					s.toolSpecs = nil
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
	if s.protocol == nil {
		return fmt.Errorf("streaming protocol %s is not supported", s.model.Protocol)
	}
	turn, err := s.protocol.OpenStream(s.ctx, protocolStreamParams{
		runID:          s.session.RunID,
		provider:       s.provider,
		model:          s.model,
		protocolConfig: s.protocolConfig,
		stageSettings:  s.stageSettings,
		messages:       s.messages,
		toolSpecs:      s.toolSpecs,
		toolChoice:     s.toolChoice,
	})
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
	if s.protocol == nil {
		return false, fmt.Errorf("streaming protocol %s is not supported", s.model.Protocol)
	}
	return s.protocol.ConsumeChunk(s, eventName, rawChunk)
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
	s.pending = append(s.pending, DeltaReasoning{Text: text, ReasoningLabel: label})
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
	format := AnyStringNode(AnyMapNode(s.protocolConfig.Compat["response"])["reasoningFormat"])
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
		if text := AnyStringNode(detail["text"]); text != "" {
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
	if result.SubmitInfo != nil {
		s.pending = append(s.pending, DeltaAwaitAnswer{
			RequestID: s.session.RequestID,
			ChatID:    s.session.ChatID,
			RunID:     s.session.RunID,
			ToolID:    result.SubmitInfo.ToolID,
			Payload:   result.SubmitInfo.Params,
		})
	}
	s.previousToolResult = structuredOrOutput(result)
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   invocation.toolID,
		ToolName: invocation.toolName,
		Result:   result,
	})
	if isPlanTool(invocation.toolName) && s.execCtx != nil && s.execCtx.PlanState != nil && len(s.execCtx.PlanState.Tasks) > 0 {
		s.pending = append(s.pending, DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   PlanTasksArray(s.execCtx.PlanState),
		})
	}
	if published, ok := result.Structured["publishedArtifacts"].([]map[string]any); ok {
		for _, item := range published {
			s.pending = append(s.pending, DeltaArtifactPublish{
				ArtifactID: AnyStringNode(item["artifactId"]),
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
				ArtifactID: AnyStringNode(item["artifactId"]),
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
	budget := NormalizeBudget(s.execCtx.Budget)
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
	viewportKey, _ := tool.Meta["viewportKey"].(string)
	viewportType, _ := tool.Meta["toolType"].(string)
	mode, _ := payload["mode"].(string)
	toolTimeout := int64(0)
	if budget := NormalizeBudget(s.execCtx.Budget); budget.Tool.TimeoutMs > 0 {
		toolTimeout = int64(budget.Tool.TimeoutMs)
	}

	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	events := make([]AgentDelta, 0, 2)
	if normalizedMode == "approval" {
		events = append(events, DeltaAwaitQuestion{
			AwaitID:      toolID,
			AwaitName:    toolName,
			ViewportType: viewportType,
			ViewportKey:  viewportKey,
			Mode:         normalizedMode,
			ToolTimeout:  toolTimeout,
			RunID:        s.session.RunID,
			ChatID:       s.session.ChatID,
			Payload:      inlineAwaitPayload(toolName, payload),
		})
	}
	if awaitPayload := deferredAwaitPayload(toolName, payload); awaitPayload != nil {
		events = append(events, DeltaAwaitPayload{
			AwaitID: toolID,
			Payload: awaitPayload,
		})
	}
	return events
}

func inlineAwaitPayload(toolName string, payload map[string]any) any {
	_, _ = toolName, payload
	return nil
}

func deferredAwaitPayload(toolName string, payload map[string]any) any {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "_ask_user_question_", "_ask_user_approval_":
		return cloneAwaitPayload(payload)
	default:
		return nil
	}
}

func cloneAwaitPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
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
