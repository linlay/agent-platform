package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/bashsec"
	"agent-platform-runner-go/internal/chat"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/hitl"
	. "agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/stream"
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
	checker             hitl.Checker

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
	hitlPendingBatch   *pendingHITLApprovalBatch
	hitlPendingCall    *preparedToolInvocation
	hitlMatch          *hitl.InterceptResult
	hitlAwaitingID     string
	hitlAwaitArgs      map[string]any
	hitlRuleWhitelist  map[string]struct{}
	pendingHITLNotices []hitlNoticeEntry
	skipPostToolHook   bool
	onApprovalSummary  func(chat.StepApproval)

	lastCallPromptTokens     int
	lastCallCompletionTokens int
	lastCallTotalTokens      int
	runPromptTokens          int
	runCompletionTokens      int
	runTotalTokens           int
	pendingUsageEmit         bool
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
	toolID              string
	toolName            string
	args                map[string]any
	prelude             []AgentDelta
	awaitExternalResult bool
	toolCallCounted     bool
	precheckedHITL      *hitl.InterceptResult
	bashSecurityReview  *bashsec.ReviewResult
	approvalID          string
	approvalDecision    string
	hitlDecision        *hitlDecisionState
	queuedResult        *ToolExecutionResult
}

type pendingHITLApprovalBatch struct {
	awaitingID  string
	awaitArgs   map[string]any
	invocations []*preparedToolInvocation
}

type hitlDecisionState struct {
	AwaitingID  string
	Decision    string
	Reason      string
	RuleKey     string
	Scope       string
	Executed    bool
	Mode        string
	FormPayload map[string]any
}

type hitlNoticeEntry struct {
	toolID      string
	command     string
	decision    string
	ruleKey     string
	reason      string
	mode        string
	formPayload map[string]any
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
		if s.hitlPendingBatch != nil {
			if err := s.awaitHITLApprovalBatchAndContinue(); err != nil {
				return err
			}
			continue
		}
		if s.hitlPendingCall != nil {
			if err := s.awaitHITLSubmitAndExecute(); err != nil {
				return err
			}
			continue
		}
		if s.activeToolCall != nil {
			toolName := s.activeToolCall.toolName
			toolID := s.activeToolCall.toolID
			if err := s.invokeActiveToolCall(); err != nil {
				return err
			}
			if s.skipPostToolHook {
				s.skipPostToolHook = false
				continue
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
	preparedRequest, err := s.protocol.PrepareRequest(protocolStreamParams{
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
	s.pending = append(s.pending, DeltaDebugPreCall{
		ChatID:                s.session.ChatID,
		ProviderKey:           s.provider.Key,
		ProviderEndpoint:      preparedRequest.Endpoint,
		ModelKey:              s.model.Key,
		ModelID:               s.model.ModelID,
		RequestBody:           preparedRequest.RequestBody,
		ContextWindow:         s.effectiveContextWindow(),
		CurrentContextSize:    s.currentContextSize(),
		EstimatedNextCallSize: s.estimatedNextCallSize(),
		RunPromptTokens:       s.runPromptTokens,
		RunCompletionTokens:   s.runCompletionTokens,
		RunTotalTokens:        s.runTotalTokens,
	})
	turn, err := s.protocol.OpenStream(s.ctx, protocolStreamParams{
		runID:          s.session.RunID,
		provider:       s.provider,
		model:          s.model,
		protocolConfig: s.protocolConfig,
		stageSettings:  s.stageSettings,
		messages:       s.messages,
		toolSpecs:      s.toolSpecs,
		toolChoice:     s.toolChoice,
	}, preparedRequest)
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

	s.emitPendingUsageDelta()

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
		if s.prepareQueuedBashApprovalBatch() {
			return nil
		}
		s.activateNextToolCall()
	}
	return nil
}

func (s *llmRunStream) currentContextSize() int {
	return s.lastCallPromptTokens
}

func (s *llmRunStream) estimatedNextCallSize() int {
	if s.lastCallPromptTokens > 0 {
		return s.lastCallPromptTokens + s.lastCallCompletionTokens + s.bytesAfterLastAssistant()/4
	}
	return s.fallbackContextEstimate()
}

// bytesAfterLastAssistant returns bytes of messages strictly after the
// last assistant message. Matches Claude Code's messages.slice(i+1) logic
// in tokenCountWithEstimation: the assistant message itself is covered by
// lastCallCompletionTokens (its output), so only tool_results / new user
// messages added since then count as "new".
func (s *llmRunStream) bytesAfterLastAssistant() int {
	lastAssistant := -1
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" {
			lastAssistant = i
			break
		}
	}
	if lastAssistant == -1 {
		return 0
	}
	newBytes := 0
	for i := lastAssistant + 1; i < len(s.messages); i++ {
		raw, _ := json.Marshal(s.messages[i])
		newBytes += len(raw)
	}
	return newBytes
}

func (s *llmRunStream) fallbackContextEstimate() int {
	total := 0
	for _, msg := range s.messages {
		raw, _ := json.Marshal(msg)
		total += len(raw)
	}
	if len(s.toolSpecs) > 0 {
		raw, _ := json.Marshal(s.toolSpecs)
		total += len(raw) / 2
	}
	return total / 4
}

const defaultContextWindow = 128000

func (s *llmRunStream) effectiveContextWindow() int {
	if s.model.ContextWindow > 0 {
		return s.model.ContextWindow
	}
	return defaultContextWindow
}

func (s *llmRunStream) emitPendingUsageDelta() {
	if !s.pendingUsageEmit {
		return
	}
	s.pendingUsageEmit = false
	s.pending = append(s.pending, DeltaDebugPostCall{
		ChatID:                    s.session.ChatID,
		ModelKey:                  s.model.Key,
		ContextWindow:             s.effectiveContextWindow(),
		CurrentContextSize:        s.currentContextSize(),
		EstimatedNextCallSize:     s.estimatedNextCallSize(),
		LLMReturnPromptTokens:     s.lastCallPromptTokens,
		LLMReturnCompletionTokens: s.lastCallCompletionTokens,
		LLMReturnTotalTokens:      s.lastCallTotalTokens,
		RunPromptTokens:           s.runPromptTokens,
		RunCompletionTokens:       s.runCompletionTokens,
		RunTotalTokens:            s.runTotalTokens,
	})
}

func (s *llmRunStream) accumulateUsage(prompt, completion, total int) {
	s.lastCallPromptTokens = prompt
	s.lastCallCompletionTokens = completion
	s.lastCallTotalTokens = total
	s.runPromptTokens += prompt
	s.runCompletionTokens += completion
	s.runTotalTokens += total
	s.pendingUsageEmit = true
	log.Printf("[llm][run:%s][usage] last-call: prompt=%d completion=%d total=%d | run-cumulative: prompt=%d completion=%d total=%d",
		s.session.RunID, prompt, completion, total, s.runPromptTokens, s.runCompletionTokens, s.runTotalTokens)
}

func (s *llmRunStream) drainUsageChunk() {
	if s.currentTurn == nil || s.currentTurn.reader == nil {
		return
	}
	for i := 0; i < 3; i++ {
		_, rawChunk, err := readSSEFrame(s.currentTurn.reader)
		if err != nil {
			break
		}
		if rawChunk == "" || rawChunk == "[DONE]" {
			break
		}
		var decoded openAIStreamResponse
		if json.Unmarshal([]byte(rawChunk), &decoded) == nil && decoded.Usage != nil {
			s.accumulateUsage(decoded.Usage.PromptTokens, decoded.Usage.CompletionTokens, decoded.Usage.TotalTokens)
			break
		}
	}
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

	if validationErr := s.validateFrontendToolArgs(toolCall.Function.Name, args); validationErr != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   "invalid tool arguments: " + validationErr.Error(),
					Error:    "invalid_tool_arguments",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    "invalid tool arguments: " + validationErr.Error(),
			}
	}
	if validationErr := validateBashToolArgs(toolCall.Function.Name, args); validationErr != nil {
		return nil, []AgentDelta{DeltaToolResult{
				ToolID:   toolID,
				ToolName: toolCall.Function.Name,
				Result: ToolExecutionResult{
					Output:   "invalid tool arguments: " + validationErr.Error(),
					Error:    "invalid_tool_arguments",
					ExitCode: -1,
				},
			}}, &openAIMessage{
				Role:       "tool",
				ToolCallID: toolID,
				Name:       toolCall.Function.Name,
				Content:    "invalid tool arguments: " + validationErr.Error(),
			}
	}

	if strings.EqualFold(strings.TrimSpace(toolCall.Function.Name), InvokeAgentsToolName) {
		rawTasks, _ := args["tasks"].([]any)
		if len(rawTasks) < 1 || len(rawTasks) > 3 {
			message := "invalid tool arguments: tasks must contain between 1 and 3 items"
			return nil, []AgentDelta{DeltaToolResult{
					ToolID:   toolID,
					ToolName: toolCall.Function.Name,
					Result: ToolExecutionResult{
						Output:   message,
						Error:    "invalid_tool_arguments",
						ExitCode: -1,
					},
				}}, &openAIMessage{
					Role:       "tool",
					ToolCallID: toolID,
					Name:       toolCall.Function.Name,
					Content:    message,
				}
		}
		tasks := make([]SubAgentTaskSpec, 0, len(rawTasks))
		for _, rawTask := range rawTasks {
			taskMap, _ := rawTask.(map[string]any)
			subAgentKey := strings.TrimSpace(mapStringArg(taskMap, "subAgentKey"))
			taskText := strings.TrimSpace(mapStringArg(taskMap, "task"))
			taskName := strings.TrimSpace(mapStringArg(taskMap, "taskName"))
			if taskName == "" {
				taskName = subAgentKey
			}
			if subAgentKey == "" || taskText == "" {
				message := "invalid tool arguments: every task requires subAgentKey and task"
				return nil, []AgentDelta{DeltaToolResult{
						ToolID:   toolID,
						ToolName: toolCall.Function.Name,
						Result: ToolExecutionResult{
							Output:   message,
							Error:    "invalid_tool_arguments",
							ExitCode: -1,
						},
					}}, &openAIMessage{
						Role:       "tool",
						ToolCallID: toolID,
						Name:       toolCall.Function.Name,
						Content:    message,
					}
			}
			tasks = append(tasks, SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
			})
		}
		groupID := "group_" + toolID
		return &preparedToolInvocation{
			toolID:              toolID,
			toolName:            toolCall.Function.Name,
			args:                args,
			awaitExternalResult: true,
			prelude: []AgentDelta{DeltaInvokeSubAgents{
				MainToolID: toolID,
				GroupID:    groupID,
				Tasks:      tasks,
			}},
		}, nil, nil
	}

	invocation := &preparedToolInvocation{
		toolID:   toolID,
		toolName: toolCall.Function.Name,
		args:     args,
		prelude:  s.preToolInvocationDeltas(toolID, toolCall.Function.Name, args),
	}
	if isBashTool(invocation.toolName) {
		review := bashsec.ReviewBashSecurity(strings.TrimSpace(mapStringArg(invocation.args, "command")))
		if review.Decision == bashsec.ReviewRequiresApproval {
			invocation.bashSecurityReview = &review
		}
	}
	return invocation, nil, nil
}

func (s *llmRunStream) validateFrontendToolArgs(toolName string, args map[string]any) error {
	tool, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return nil
	}
	toolKind, _ := tool.Meta["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(toolKind), "frontend") {
		return nil
	}
	if s.engine.frontend == nil {
		return nil
	}
	handler, ok := s.engine.frontend.Handler(toolName)
	if !ok {
		return nil
	}
	return handler.ValidateArgs(args)
}

func validateBashToolArgs(toolName string, args map[string]any) error {
	if !isBashTool(toolName) {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "command")) == "" {
		return nil
	}
	if strings.TrimSpace(mapStringArg(args, "description")) == "" {
		return fmt.Errorf("description is required for bash tools")
	}
	return nil
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
	s.skipPostToolHook = false

	s.execCtx.CurrentToolID = invocation.toolID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}
	if !invocation.toolCallCounted {
		s.execCtx.ToolCalls++
		invocation.toolCallCounted = true
	}
	keepActive := false
	defer func() {
		if keepActive {
			return
		}
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(invocation.toolID)
		}
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		s.activeToolCall = nil
	}()
	if invocation.queuedResult != nil {
		s.appendOriginalToolResult(invocation, *invocation.queuedResult)
		invocation.queuedResult = nil
		return nil
	}
	if invocation.awaitExternalResult {
		keepActive = true
		return nil
	}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		if strings.TrimSpace(invocation.approvalDecision) != "" {
			return s.executeApprovedBashSecurityInvocation(invocation, review)
		}
		s.skipPostToolHook = true
		return s.emitBashSecurityApprovalDeltas(invocation, review)
	}
	if invocation.precheckedHITL != nil && invocation.precheckedHITL.Intercepted {
		result := *invocation.precheckedHITL
		if strings.EqualFold(result.Rule.ViewportType, "builtin") {
			if strings.TrimSpace(invocation.approvalDecision) != "" {
				return s.executeApprovedBashInvocation(invocation, result)
			}
			if s.isRuleWhitelisted(result.Rule.RuleKey) {
				s.applyHITLDecision(invocation, result, "", "approve_prefix_run", "", true)
				return s.executeApprovedBashInvocation(invocation, result)
			}
			if s.shouldAutoApproveHITL(result) {
				return s.executeOriginalBash(invocation)
			}
		}
		s.skipPostToolHook = true
		return s.emitHITLConfirmDeltas(invocation, result)
	}
	if s.checker != nil && isBashTool(invocation.toolName) {
		command := mapStringArg(invocation.args, "command")
		if result := s.checker.Check(command, s.execCtx.HITLLevel); result.Intercepted {
			s.skipPostToolHook = true
			if strings.EqualFold(result.Rule.ViewportType, "builtin") && s.isRuleWhitelisted(result.Rule.RuleKey) {
				s.applyHITLDecision(invocation, result, "", "approve_prefix_run", "", true)
				return s.executeApprovedBashInvocation(invocation, result)
			}
			if s.shouldAutoApproveHITL(result) {
				return s.executeOriginalBash(invocation)
			}
			return s.emitHITLConfirmDeltas(invocation, result)
		}
	}

	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, s.execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	if result.SubmitInfo != nil {
		s.pending = append(s.pending, DeltaRequestSubmit{
			RequestID:  s.session.RequestID,
			ChatID:     s.session.ChatID,
			RunID:      s.session.RunID,
			AwaitingID: result.SubmitInfo.AwaitingID,
			Params:     result.SubmitInfo.Params,
		})
		if answer := frontendSubmitAwaitingAnswer(invocation, result); len(answer) > 0 {
			s.pending = append(s.pending, DeltaAwaitingAnswer{
				AwaitingID: result.SubmitInfo.AwaitingID,
				Answer:     CloneMap(answer),
			})
		}
	} else if len(result.Structured) > 0 {
		if answer := frontendSubmitAwaitingAnswer(invocation, result); len(answer) > 0 {
			s.pending = append(s.pending, DeltaAwaitingAnswer{
				AwaitingID: invocation.toolID,
				Answer:     CloneMap(answer),
			})
		}
	}
	s.appendOriginalToolResult(invocation, result)
	if isPlanTool(invocation.toolName) && s.execCtx != nil && s.execCtx.PlanState != nil && len(s.execCtx.PlanState.Tasks) > 0 {
		s.pending = append(s.pending, DeltaPlanUpdate{
			PlanID: s.execCtx.PlanState.PlanID,
			ChatID: s.session.ChatID,
			Plan:   PlanTasksArray(s.execCtx.PlanState),
		})
	}
	appendPublishedArtifactDelta(&s.pending, s.session, result.Structured["publishedArtifacts"])
	return nil
}

func appendPublishedArtifactDelta(pending *[]AgentDelta, session QuerySession, raw any) {
	published := publishedArtifactMaps(raw)
	if len(published) == 0 {
		return
	}
	*pending = append(*pending, DeltaArtifactPublish{
		ChatID:        session.ChatID,
		RunID:         session.RunID,
		ArtifactCount: len(published),
		Artifacts:     published,
	})
}

func publishedArtifactMaps(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if len(item) == 0 {
				continue
			}
			items = append(items, CloneMap(item))
		}
		return items
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, rawItem := range typed {
			item, _ := rawItem.(map[string]any)
			if len(item) == 0 {
				continue
			}
			items = append(items, CloneMap(item))
		}
		return items
	default:
		return nil
	}
}

func (s *llmRunStream) lookupBashSecurityReview(invocation *preparedToolInvocation) bashsec.ReviewResult {
	if invocation == nil || !isBashTool(invocation.toolName) {
		return bashsec.ReviewResult{Decision: bashsec.ReviewAllow}
	}
	if invocation.bashSecurityReview != nil {
		return *invocation.bashSecurityReview
	}
	review := bashsec.ReviewBashSecurity(strings.TrimSpace(mapStringArg(invocation.args, "command")))
	if review.Decision == bashsec.ReviewRequiresApproval {
		cloned := review
		invocation.bashSecurityReview = &cloned
	}
	return review
}

func (s *llmRunStream) emitBashSecurityApprovalDeltas(invocation *preparedToolInvocation, review bashsec.ReviewResult) error {
	result := bashSecurityInterceptResult(invocation, review)
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.buildConfirmApprovalArgs(invocation)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args))

	if s.runControl != nil {
		awaitDelta, _ := s.pending[len(s.pending)-1].(DeltaAwaitAsk)
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	s.activeToolCall = nil
	if s.execCtx != nil {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
	}
	return nil
}

func (s *llmRunStream) executeApprovedBashSecurityInvocation(invocation *preparedToolInvocation, review bashsec.ReviewResult) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve", "approve_prefix_run":
		invocation.approvalDecision = ""
		s.registerBashSecurityApproval(review.Fingerprint)
		return s.executeOriginalBash(invocation)
	default:
		return s.emitBashSecurityApprovalDeltas(invocation, review)
	}
}

func (s *llmRunStream) registerBashSecurityApproval(fingerprint string) {
	if s.execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if s.execCtx.BashSecurityApprovals == nil {
		s.execCtx.BashSecurityApprovals = map[string]int{}
	}
	s.execCtx.BashSecurityApprovals[fingerprint]++
}

func bashSecurityInterceptResult(invocation *preparedToolInvocation, review bashsec.ReviewResult) hitl.InterceptResult {
	command := ""
	if invocation != nil {
		command = strings.TrimSpace(mapStringArg(invocation.args, "command"))
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey:      "bash-security::" + review.Fingerprint,
			Level:        1,
			Title:        "Bash security approval",
			ViewportType: "builtin",
			ViewportKey:  "confirm_dialog",
		},
		OriginalCommand: command,
		MatchedCommand:  command,
		MatchedWhole:    true,
	}
}

func (s *llmRunStream) approvalHITLResult(invocation *preparedToolInvocation) hitl.InterceptResult {
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return bashSecurityInterceptResult(invocation, review)
	}
	return s.lookupPrecheckedHITL(invocation)
}

func (s *llmRunStream) executeApprovedBashInvocation(invocation *preparedToolInvocation, result hitl.InterceptResult) error {
	switch strings.ToLower(strings.TrimSpace(invocation.approvalDecision)) {
	case "reject":
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	case "approve_prefix_run":
		s.registerRuleWhitelist(result.Rule.RuleKey)
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	case "approve":
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation)
	default:
		return s.executeOriginalBash(invocation)
	}
}

func (s *llmRunStream) shouldAutoApproveHITL(result hitl.InterceptResult) bool {
	if s.execCtx == nil || !strings.EqualFold(result.Rule.ViewportType, "builtin") {
		return false
	}
	if len(s.execCtx.AutoApproveLevels) == 0 {
		return false
	}
	return s.execCtx.AutoApproveLevels[result.Rule.Level]
}

func (s *llmRunStream) emitHITLConfirmDeltas(invocation *preparedToolInvocation, result hitl.InterceptResult) error {
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.buildHITLArgs(invocation, result)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args))

	if s.runControl != nil {
		awaitDelta, _ := s.pending[len(s.pending)-1].(DeltaAwaitAsk)
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	s.activeToolCall = nil
	s.execCtx.CurrentToolID = ""
	s.execCtx.CurrentToolName = ""
	return nil
}

func (s *llmRunStream) prepareQueuedBashApprovalBatch() bool {
	if len(s.queuedToolCalls) == 0 || s.hitlPendingBatch != nil || s.hitlPendingCall != nil {
		return false
	}

	approvals := make([]any, 0)
	invocations := make([]*preparedToolInvocation, 0)
	for _, invocation := range s.queuedToolCalls {
		if !isBashTool(invocation.toolName) {
			continue
		}
		if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
			approvals = append(approvals, s.buildApprovalAskItem(invocation))
			invocations = append(invocations, invocation)
			continue
		}
		if s.checker == nil {
			continue
		}
		result := s.lookupPrecheckedHITL(invocation)
		if !result.Intercepted {
			continue
		}
		if !strings.EqualFold(result.Rule.ViewportType, "builtin") {
			continue
		}
		if s.isRuleWhitelisted(result.Rule.RuleKey) {
			s.applyHITLDecision(invocation, result, "", "approve_prefix_run", "", true)
			continue
		}
		if s.shouldAutoApproveHITL(result) {
			continue
		}
		approvals = append(approvals, s.buildApprovalAskItem(invocation))
		invocations = append(invocations, invocation)
	}
	if len(invocations) == 0 {
		return false
	}

	awaitingID := buildHITLBatchAwaitingID(s.session.RunID, s.step)
	args := map[string]any{
		"mode":      "approval",
		"approvals": approvals,
	}
	s.hitlPendingBatch = &pendingHITLApprovalBatch{
		awaitingID:  awaitingID,
		awaitArgs:   CloneMap(args),
		invocations: invocations,
	}
	awaitDelta := s.buildHITLAwaitDelta(awaitingID, args)
	s.pending = append(s.pending, awaitDelta)
	if s.runControl != nil {
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	return true
}

func (s *llmRunStream) lookupPrecheckedHITL(invocation *preparedToolInvocation) hitl.InterceptResult {
	if invocation == nil {
		return hitl.InterceptResult{}
	}
	if invocation.precheckedHITL != nil {
		return *invocation.precheckedHITL
	}
	command := mapStringArg(invocation.args, "command")
	hitlLevel := 0
	if s.execCtx != nil {
		hitlLevel = s.execCtx.HITLLevel
	}
	result := s.checker.Check(command, hitlLevel)
	if result.Intercepted {
		cloned := result
		invocation.precheckedHITL = &cloned
	}
	return result
}

func (s *llmRunStream) awaitHITLApprovalBatchAndContinue() error {
	batch := s.hitlPendingBatch
	if batch == nil || strings.TrimSpace(batch.awaitingID) == "" {
		s.hitlPendingBatch = nil
		return nil
	}
	defer func() {
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(batch.awaitingID)
		}
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		s.hitlPendingBatch = nil
	}()
	if s.runControl == nil {
		return ErrRunControlUnavailable
	}

	s.execCtx.CurrentToolID = batch.awaitingID
	s.execCtx.CurrentToolName = "bash"
	s.execCtx.RunLoopState = RunLoopStateWaitingSubmit
	s.runControl.TransitionState(RunLoopStateWaitingSubmit)

	submitResult, err := s.runControl.AwaitSubmitWithTimeout(
		s.ctx,
		batch.awaitingID,
		time.Duration(s.resolveHITLTimeout())*time.Millisecond,
	)
	if err != nil {
		if errors.Is(err, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: batch.awaitingID,
			Answer:     hitlTimeoutAnswer("approval"),
		})
		for _, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", "timeout", false)
			timeoutResult := hitlTimeoutToolResult(invocation)
			invocation.queuedResult = &timeoutResult
		}
		s.hitlPendingBatch = nil
		return nil
	}

	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	s.runControl.TransitionState(RunLoopStateToolExecuting)
	s.pending = append(s.pending, DeltaRequestSubmit{
		RequestID:  s.session.RequestID,
		ChatID:     s.session.ChatID,
		RunID:      s.session.RunID,
		AwaitingID: batch.awaitingID,
		Params:     submitResult.Request.Params,
	})

	normalized, normalizeErr := s.normalizeHITLSubmit(batch.awaitArgs, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: batch.awaitingID,
			Answer:     AwaitingErrorAnswer(strings.TrimSpace(AnyStringNode(batch.awaitArgs["mode"])), "invalid_submit", normalizeErr.Error()),
		})
		for _, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", normalizeErr.Error(), false)
			result := frontendSubmitInvalidPayloadResult(invocation, batch.awaitingID, submitResult.Request.Params, normalizeErr)
			invocation.queuedResult = &result
		}
		s.hitlPendingBatch = nil
		return nil
	}
	if len(normalized) > 0 {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: batch.awaitingID,
			Answer:     CloneMap(normalized),
		})
	}

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		for _, invocation := range batch.invocations {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", "user_dismissed", false)
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
		}
		s.hitlPendingBatch = nil
		return nil
	}

	approvals, _ := normalized["approvals"].([]map[string]any)
	for index, invocation := range batch.invocations {
		if index >= len(approvals) {
			s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, "reject", "", false)
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
			continue
		}
		normalizedDecision := strings.TrimSpace(AnyStringNode(approvals[index]["decision"]))
		reason := strings.TrimSpace(AnyStringNode(approvals[index]["reason"]))
		s.applyHITLDecision(invocation, s.approvalHITLResult(invocation), batch.awaitingID, normalizedDecision, reason, normalizedDecision != "reject")
		invocation.approvalDecision = normalizedDecision
		if strings.EqualFold(normalizedDecision, "reject") {
			rejected := hitlRejectedToolResult(invocation)
			invocation.queuedResult = &rejected
		}
	}
	s.hitlPendingBatch = nil
	return nil
}

func (s *llmRunStream) awaitHITLSubmitAndExecute() error {
	invocation := s.hitlPendingCall
	match := s.hitlMatch
	awaitingID := s.hitlAwaitingID
	awaitArgs := CloneMap(s.hitlAwaitArgs)
	if invocation == nil || match == nil || awaitingID == "" {
		s.hitlPendingCall = nil
		s.hitlMatch = nil
		s.hitlAwaitingID = ""
		s.hitlAwaitArgs = nil
		return nil
	}
	defer func() {
		s.hitlPendingCall = nil
		s.hitlMatch = nil
		s.hitlAwaitingID = ""
		s.hitlAwaitArgs = nil
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(awaitingID)
		}
	}()
	if s.runControl == nil {
		return ErrRunControlUnavailable
	}

	s.execCtx.CurrentToolID = awaitingID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateWaitingSubmit
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateWaitingSubmit)
	}

	submitResult, err := s.runControl.AwaitSubmitWithTimeout(s.ctx, awaitingID, time.Duration(s.resolveHITLTimeout())*time.Millisecond)
	if err != nil {
		if errors.Is(err, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     hitlTimeoutAnswer(strings.TrimSpace(AnyStringNode(awaitArgs["mode"]))),
		})
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", "timeout", false)
		s.appendOriginalToolResult(invocation, hitlTimeoutToolResult(invocation))
		return nil
	}

	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}

	s.pending = append(s.pending, DeltaRequestSubmit{
		RequestID:  s.session.RequestID,
		ChatID:     s.session.ChatID,
		RunID:      s.session.RunID,
		AwaitingID: awaitingID,
		Params:     submitResult.Request.Params,
	})

	normalized, normalizeErr := s.normalizeHITLSubmit(awaitArgs, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     AwaitingErrorAnswer(strings.TrimSpace(AnyStringNode(awaitArgs["mode"])), "invalid_submit", normalizeErr.Error()),
		})
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", normalizeErr.Error(), false)
		s.appendOriginalToolResult(invocation, frontendSubmitInvalidPayloadResult(invocation, awaitingID, submitResult.Request.Params, normalizeErr))
		return nil
	}
	if len(normalized) > 0 {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     CloneMap(normalized),
		})
	}

	if strings.EqualFold(AnyStringNode(normalized["status"]), "error") {
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", "user_dismissed", false)
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	}

	if strings.EqualFold(AnyStringNode(normalized["mode"]), "form") {
		selectedForm := firstAwaitItem(normalized["forms"])
		action := strings.ToLower(strings.TrimSpace(AnyStringNode(selectedForm["action"])))
		if action == "submit" {
			formPayload := AnyMapNode(selectedForm["payload"])
			rebuiltCommand, rebuildErr := reconstructCommandWithPayload(mapStringArg(invocation.args, "command"), formPayload)
			if rebuildErr != nil {
				payload := NewErrorPayload(
					"frontend_submit_invalid_payload",
					rebuildErr.Error(),
					ErrorScopeFrontendSubmit,
					ErrorCategoryTool,
					map[string]any{
						"awaitingId": awaitingID,
						"toolName":   invocation.toolName,
						"payload":    formPayload,
					},
				)
				result := ToolExecutionResult{
					Output:     marshalJSON(payload),
					Structured: payload,
					Error:      "frontend_submit_invalid_payload",
					ExitCode:   -1,
				}
				s.applyHITLDecision(invocation, *match, awaitingID, "reject", rebuildErr.Error(), false)
				s.appendOriginalToolResult(invocation, result)
				return nil
			}
			invocation.args["command"] = rebuiltCommand
			s.applyHITLDecision(invocation, *match, awaitingID, "approve", "", true)
			invocation.hitlDecision.FormPayload = formPayload
			return s.executeOriginalBash(invocation)
		}
		s.applyHITLDecision(invocation, *match, awaitingID, "reject", strings.TrimSpace(AnyStringNode(selectedForm["reason"])), false)
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	}

	selectedApproval := firstAwaitItem(normalized["approvals"])
	selectedDecision := strings.TrimSpace(AnyStringNode(selectedApproval["decision"]))
	reason := strings.TrimSpace(AnyStringNode(selectedApproval["reason"]))
	s.applyHITLDecision(invocation, *match, awaitingID, selectedDecision, reason, selectedDecision != "reject")
	if strings.EqualFold(selectedDecision, "reject") {
		s.appendOriginalToolResult(invocation, hitlRejectedToolResult(invocation))
		return nil
	}
	invocation.approvalDecision = selectedDecision
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return s.executeApprovedBashSecurityInvocation(invocation, review)
	}
	return s.executeOriginalBash(invocation)
}

func (s *llmRunStream) executeOriginalBash(invocation *preparedToolInvocation) error {
	s.execCtx.CurrentToolID = invocation.toolID
	s.execCtx.CurrentToolName = invocation.toolName
	s.execCtx.RunLoopState = RunLoopStateToolExecuting
	if s.runControl != nil {
		s.runControl.TransitionState(RunLoopStateToolExecuting)
	}

	result, invokeErr := s.engine.tools.Invoke(s.ctx, invocation.toolName, invocation.args, s.execCtx)
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrRunInterrupted) {
			return s.handleInterruptIfNeeded()
		}
		result = ToolExecutionResult{Output: invokeErr.Error(), Error: "tool_execution_failed", ExitCode: -1}
	}
	s.appendOriginalToolResult(invocation, result)
	s.execCtx.CurrentToolID = ""
	s.execCtx.CurrentToolName = ""
	return nil
}

func (s *llmRunStream) buildHITLArgs(invocation *preparedToolInvocation, result hitl.InterceptResult) map[string]any {
	command := mapStringArg(invocation.args, "command")
	if strings.EqualFold(result.Rule.ViewportType, "html") {
		return s.buildFormApprovalArgs(command, result)
	}
	return s.buildConfirmApprovalArgs(invocation)
}

func (s *llmRunStream) buildConfirmApprovalArgs(invocation *preparedToolInvocation) map[string]any {
	return map[string]any{
		"mode": "approval",
		"approvals": []any{
			s.buildApprovalAskItem(invocation),
		},
	}
}

func (s *llmRunStream) buildFormApprovalArgs(command string, result hitl.InterceptResult) map[string]any {
	args := map[string]any{
		"mode":         "form",
		"viewportType": result.Rule.ViewportType,
		"viewportKey":  result.Rule.ViewportKey,
	}
	form := map[string]any{
		"id":      "form-1",
		"command": command,
	}
	if title := strings.TrimSpace(result.Rule.Title); title != "" {
		form["title"] = title
	}
	if payload := extractCommandPayload(result.ParsedCommand); len(payload) > 0 {
		form["payload"] = payload
		args["forms"] = []any{form}
		return args
	}
	if payload := extractPayloadFromOriginalCommand(result.OriginalCommand); len(payload) > 0 {
		form["payload"] = payload
		args["forms"] = []any{form}
		return args
	}
	args["forms"] = []any{form}
	log.Printf("[llm][run:%s][hitl][warning] missing html approval payload viewportKey=%s command=%q",
		s.session.RunID,
		result.Rule.ViewportKey,
		result.OriginalCommand,
	)
	return args
}

func (s *llmRunStream) buildApprovalAskItem(invocation *preparedToolInvocation) map[string]any {
	item := map[string]any{
		"id":                  invocation.toolID,
		"command":             mapStringArg(invocation.args, "command"),
		"description":         approvalDescription(invocation),
		"options":             s.approvalOptionsForInvocation(invocation),
		"allowFreeText":       true,
		"freeTextPlaceholder": "可选：填写理由",
	}
	result := hitl.InterceptResult{}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		result = bashSecurityInterceptResult(invocation, review)
	} else if invocation != nil && invocation.precheckedHITL != nil {
		result = *invocation.precheckedHITL
	} else if s.checker != nil {
		result = s.lookupPrecheckedHITL(invocation)
	}
	if result.Intercepted {
		if ruleKey := strings.TrimSpace(result.Rule.RuleKey); ruleKey != "" {
			item["ruleKey"] = ruleKey
		}
	}
	return item
}

func (s *llmRunStream) approvalOptionsForInvocation(invocation *preparedToolInvocation) []any {
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return buildSingleUseApprovalOptions()
	}
	return buildApprovalOptions()
}

func buildSingleUseApprovalOptions() []any {
	return []any{
		map[string]any{
			"label":       "同意",
			"decision":    "approve",
			"description": "只本次放行这条命令",
		},
		map[string]any{
			"label":       "拒绝",
			"decision":    "reject",
			"description": "终止这条命令",
		},
	}
}

func buildApprovalOptions() []any {
	return []any{
		map[string]any{
			"label":       "同意",
			"decision":    "approve",
			"description": "只本次放行这条命令",
		},
		map[string]any{
			"label":       "同意（本次运行同前缀都放行）",
			"decision":    "approve_prefix_run",
			"description": "本次 run 内所有同一拦截规则命中的命令自动放行，不再询问",
		},
		map[string]any{
			"label":       "拒绝",
			"decision":    "reject",
			"description": "终止这条命令",
		},
	}
}

func approvalDescription(invocation *preparedToolInvocation) string {
	description := strings.TrimSpace(mapStringArg(invocation.args, "description"))
	if description != "" {
		return description
	}
	command := strings.TrimSpace(mapStringArg(invocation.args, "command"))
	if len(command) <= 60 {
		return command
	}
	return command[:60]
}

func (s *llmRunStream) resolveHITLTimeout() int64 {
	if s.engine.cfg.BashHITL.DefaultTimeoutMs > 0 {
		return int64(s.engine.cfg.BashHITL.DefaultTimeoutMs)
	}
	budget := NormalizeBudget(s.execCtx.Budget)
	if budget.Tool.TimeoutMs > 0 {
		return int64(budget.Tool.TimeoutMs)
	}
	return 120000
}

func (s *llmRunStream) appendOriginalToolResult(invocation *preparedToolInvocation, result ToolExecutionResult) {
	result = applyHITLMetadata(result, invocation)
	s.previousToolResult = structuredOrOutput(result)
	s.pending = append(s.pending, DeltaToolResult{
		ToolID:   invocation.toolID,
		ToolName: invocation.toolName,
		Result:   result,
	})
	s.messages = append(s.messages, openAIMessage{
		Role:       "tool",
		ToolCallID: invocation.toolID,
		Name:       invocation.toolName,
		Content:    s.toolResultContent(invocation.toolName, result),
	})
	if entry, ok := buildHITLNoticeEntry(invocation); ok {
		s.pendingHITLNotices = append(s.pendingHITLNotices, entry)
	}
	if len(s.queuedToolCalls) == 0 && len(s.pendingHITLNotices) > 0 {
		summary, approval := buildHITLBatchSummaryAndApproval(s.pendingHITLNotices)
		if summary != "" {
			s.messages = append(s.messages, openAIMessage{
				Role:    "user",
				Content: summary,
			})
		}
		if s.onApprovalSummary != nil && approval != nil {
			s.onApprovalSummary(*approval)
		}
		s.pendingHITLNotices = nil
	}
}

func applyHITLMetadata(result ToolExecutionResult, invocation *preparedToolInvocation) ToolExecutionResult {
	if invocation == nil || invocation.hitlDecision == nil {
		return result
	}
	switch strings.ToLower(strings.TrimSpace(invocation.hitlDecision.Mode)) {
	case "approval":
		result.HITL = buildHITLApprovalPayload(invocation.hitlDecision)
	case "form":
		result.HITL = buildHITLFormPayload(invocation.hitlDecision)
	}
	return result
}

func buildHITLApprovalPayload(decision *hitlDecisionState) map[string]any {
	if decision == nil {
		return nil
	}
	payload := map[string]any{
		"decision": decision.Decision,
	}
	if awaitingID := strings.TrimSpace(decision.AwaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	if ruleKey := strings.TrimSpace(decision.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		payload["reason"] = reason
	}
	return payload
}

func buildHITLFormPayload(decision *hitlDecisionState) map[string]any {
	if decision == nil {
		return nil
	}
	payload := map[string]any{
		"mode":     "form",
		"decision": decision.Decision,
	}
	if awaitingID := strings.TrimSpace(decision.AwaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	if ruleKey := strings.TrimSpace(decision.RuleKey); ruleKey != "" {
		payload["ruleKey"] = ruleKey
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		payload["reason"] = reason
	}
	if decision.FormPayload != nil {
		payload["submittedPayload"] = decision.FormPayload
	}
	return payload
}

func buildHITLNoticeEntry(invocation *preparedToolInvocation) (hitlNoticeEntry, bool) {
	if invocation == nil || invocation.hitlDecision == nil {
		return hitlNoticeEntry{}, false
	}
	mode := strings.ToLower(strings.TrimSpace(invocation.hitlDecision.Mode))
	if mode != "approval" && mode != "form" {
		return hitlNoticeEntry{}, false
	}
	return hitlNoticeEntry{
		toolID:      invocation.toolID,
		command:     mapStringArg(invocation.args, "command"),
		decision:    invocation.hitlDecision.Decision,
		ruleKey:     invocation.hitlDecision.RuleKey,
		reason:      invocation.hitlDecision.Reason,
		mode:        mode,
		formPayload: invocation.hitlDecision.FormPayload,
	}, true
}

func formatHITLBatchSummary(entries []hitlNoticeEntry) string {
	if len(entries) == 0 {
		return ""
	}
	if len(entries) == 1 {
		return "[HITL] " + formatHITLSummaryLine(entries[0])
	}

	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, "[HITL] 审批结果：")
	for index, entry := range entries {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, formatHITLSummaryLine(entry)))
	}
	return strings.Join(lines, "\n")
}

func formatHITLSummaryLine(entry hitlNoticeEntry) string {
	if entry.mode == "form" {
		return formatHITLFormSummaryLine(entry)
	}
	line := strings.TrimSpace(entry.command) + " → " + strings.TrimSpace(entry.decision)
	if reason := strings.TrimSpace(entry.reason); reason != "" {
		line += "（" + reason + "）"
	}
	return line
}

func formatHITLFormSummaryLine(entry hitlNoticeEntry) string {
	line := strings.TrimSpace(entry.command) + " → " + strings.TrimSpace(entry.decision)
	if reason := strings.TrimSpace(entry.reason); reason != "" {
		line += "（" + reason + "）"
	}
	if strings.EqualFold(entry.decision, "approve") && entry.formPayload != nil {
		if payloadJSON, err := json.Marshal(entry.formPayload); err == nil {
			line += "\n  提交参数: " + string(payloadJSON)
		}
	}
	return line
}

func buildHITLBatchSummaryAndApproval(entries []hitlNoticeEntry) (string, *chat.StepApproval) {
	summary := formatHITLBatchSummary(entries)
	if summary == "" {
		return "", nil
	}

	approval := &chat.StepApproval{
		Summary:   summary,
		Decisions: make([]chat.StepApprovalDecision, 0, len(entries)),
	}
	for _, entry := range entries {
		approval.Decisions = append(approval.Decisions, chat.StepApprovalDecision{
			ToolID:   entry.toolID,
			Command:  entry.command,
			Decision: entry.decision,
			RuleKey:  strings.TrimSpace(entry.ruleKey),
			Reason:   entry.reason,
			Mode:     entry.mode,
			Payload:  entry.formPayload,
		})
	}
	return summary, approval
}

func (s *llmRunStream) applyHITLDecision(invocation *preparedToolInvocation, result hitl.InterceptResult, awaitingID string, decision string, reason string, executed bool) {
	if invocation == nil {
		return
	}
	normalizedDecision := strings.ToLower(strings.TrimSpace(decision))
	if normalizedDecision == "" {
		normalizedDecision = "reject"
	}
	invocation.approvalDecision = normalizedDecision
	invocation.hitlDecision = &hitlDecisionState{
		AwaitingID: strings.TrimSpace(awaitingID),
		Decision:   normalizedDecision,
		Reason:     strings.TrimSpace(reason),
		RuleKey:    strings.TrimSpace(result.Rule.RuleKey),
		Scope:      hitlDecisionScope(normalizedDecision),
		Executed:   executed,
		Mode:       hitlDecisionMode(result),
	}
	if normalizedDecision == "approve_prefix_run" {
		s.registerRuleWhitelist(result.Rule.RuleKey)
	}
}

func hitlDecisionScope(decision string) string {
	if strings.EqualFold(strings.TrimSpace(decision), "approve_prefix_run") {
		return "run_rule"
	}
	return ""
}

func hitlDecisionMode(result hitl.InterceptResult) string {
	if strings.EqualFold(strings.TrimSpace(result.Rule.ViewportType), "builtin") {
		return "approval"
	}
	return "form"
}

func (s *llmRunStream) isRuleWhitelisted(ruleKey string) bool {
	if strings.TrimSpace(ruleKey) == "" || len(s.hitlRuleWhitelist) == 0 {
		return false
	}
	_, ok := s.hitlRuleWhitelist[strings.TrimSpace(ruleKey)]
	return ok
}

func (s *llmRunStream) registerRuleWhitelist(ruleKey string) {
	ruleKey = strings.TrimSpace(ruleKey)
	if ruleKey == "" {
		return
	}
	if s.hitlRuleWhitelist == nil {
		s.hitlRuleWhitelist = map[string]struct{}{}
	}
	s.hitlRuleWhitelist[ruleKey] = struct{}{}
}

func (s *llmRunStream) toolResultContent(toolName string, result ToolExecutionResult) string {
	toolDef, ok := s.lookupToolDefinition(toolName)
	if !ok {
		return result.Output
	}
	return formatSubmitResultForLLM(toolDef, s.engine.frontend, result)
}

func isBashTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "simple-bash":
		return true
	default:
		return false
	}
}

func mapStringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func structuredResult(payload map[string]any) ToolExecutionResult {
	data, _ := json.Marshal(payload)
	return ToolExecutionResult{
		Output:     string(data),
		Structured: payload,
		ExitCode:   0,
	}
}

func buildHITLAwaitingID(toolID string) string {
	return "await_" + strings.TrimSpace(toolID)
}

func buildHITLBatchAwaitingID(runID string, turnStep int) string {
	return fmt.Sprintf("await_batch_%s_%d", strings.TrimSpace(runID), turnStep)
}

func hitlTimeoutAnswer(mode string) map[string]any {
	return AwaitingErrorAnswer(mode, "timeout", "等待项已超时")
}

func frontendSubmitAwaitingAnswer(invocation *preparedToolInvocation, result ToolExecutionResult) map[string]any {
	if len(result.Structured) == 0 {
		return nil
	}
	if result.Error == "" {
		return result.Structured
	}
	mode := strings.TrimSpace(AnyStringNode(invocation.args["mode"]))
	switch result.Error {
	case "frontend_submit_timeout":
		return AwaitingErrorAnswer(mode, "timeout", AnyStringNode(AnyMapNode(result.Structured["error"])["message"]))
	case "frontend_submit_invalid_payload":
		return AwaitingErrorAnswer(mode, "invalid_submit", AnyStringNode(result.Structured["message"]))
	default:
		return nil
	}
}

func hitlRejectedToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := NewErrorPayload(
		"hitl_rejected",
		"User rejected this command. Do NOT retry with a different command. End the turn now.",
		ErrorScopeTool,
		ErrorCategorySystem,
		map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		},
	)
	payload["final"] = true
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("user_rejected", "User rejected this command. Do NOT retry with a different command. End the turn now."),
		Structured: payload,
		Error:      "user_rejected",
		ExitCode:   -1,
	}
}

func hitlTimeoutToolResult(invocation *preparedToolInvocation) ToolExecutionResult {
	payload := NewErrorPayload(
		"hitl_timeout",
		"command execution timed out while waiting for user approval",
		ErrorScopeTool,
		ErrorCategoryTimeout,
		map[string]any{
			"toolId":   invocation.toolID,
			"toolName": invocation.toolName,
		},
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("hitl_timeout", "command execution timed out while waiting for user approval"),
		Structured: payload,
		Error:      "hitl_timeout",
		ExitCode:   -1,
	}
}

func frontendSubmitInvalidPayloadResult(invocation *preparedToolInvocation, awaitingID string, params any, err error) ToolExecutionResult {
	payload := NewErrorPayload(
		"frontend_submit_invalid_payload",
		err.Error(),
		ErrorScopeFrontendSubmit,
		ErrorCategoryTool,
		map[string]any{
			"awaitingId": awaitingID,
			"toolName":   invocation.toolName,
			"params":     params,
		},
	)
	return ToolExecutionResult{
		Output:     formatToolErrorOutput("frontend_submit_invalid_payload", err.Error()),
		Structured: payload,
		Error:      "frontend_submit_invalid_payload",
		ExitCode:   -1,
	}
}

func (s *llmRunStream) buildHITLAwaitDelta(awaitingID string, args map[string]any) DeltaAwaitAsk {
	await := DeltaAwaitAsk{
		AwaitingID: awaitingID,
		Mode:       strings.ToLower(strings.TrimSpace(AnyStringNode(args["mode"]))),
		Timeout:    s.resolveHITLTimeout(),
		RunID:      s.session.RunID,
	}
	if await.Mode == "form" {
		await.ViewportType = strings.TrimSpace(AnyStringNode(args["viewportType"]))
		await.ViewportKey = strings.TrimSpace(AnyStringNode(args["viewportKey"]))
	}
	if questions := cloneAnySlice(args["questions"]); len(questions) > 0 {
		await.Questions = questions
	}
	if approvals := cloneAnySlice(args["approvals"]); len(approvals) > 0 {
		await.Approvals = approvals
	}
	if forms := cloneAnySlice(args["forms"]); len(forms) > 0 {
		await.Forms = sanitizeAwaitAskForms(forms)
	}
	return await
}

func sanitizeAwaitAskForms(forms []any) []any {
	cloned := make([]any, 0, len(forms))
	for _, item := range forms {
		form := AnyMapNode(item)
		if len(form) == 0 {
			continue
		}
		entry := CloneMap(form)
		delete(entry, "command")
		cloned = append(cloned, entry)
	}
	return cloned
}

func cloneAnySlice(raw any) []any {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case map[string]any:
			cloned = append(cloned, CloneMap(value))
		default:
			cloned = append(cloned, value)
		}
	}
	return cloned
}

func firstAwaitItem(raw any) map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		for _, item := range typed {
			if len(item) > 0 {
				return item
			}
		}
	case []any:
		for _, item := range typed {
			entry := AnyMapNode(item)
			if len(entry) > 0 {
				return entry
			}
		}
	}
	return nil
}

func resolveApprovedCommand(value string, answer string) string {
	switch {
	case strings.EqualFold(value, "approve"), strings.EqualFold(value, "approve_prefix_run"), strings.EqualFold(value, "reject"):
		return ""
	case value != "":
		return value
	default:
		return answer
	}
}

func (s *llmRunStream) normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	return normalizeHITLSubmit(args, params)
}

func extractCommandPayload(parsed hitl.CommandComponents) map[string]any {
	for idx := 0; idx < len(parsed.Tokens)-1; idx++ {
		if strings.TrimSpace(parsed.Tokens[idx]) != "--payload" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(parsed.Tokens[idx+1]), &payload); err != nil {
			return nil
		}
		if payload == nil {
			return nil
		}
		return payload
	}
	return nil
}

func extractPayloadFromOriginalCommand(command string) map[string]any {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	tokenSpans := firstSegmentTokenSpans(command)
	for idx := 0; idx < len(tokenSpans)-1; idx++ {
		if strings.TrimSpace(tokenSpans[idx].Text) != "--payload" {
			continue
		}
		rawToken := strings.TrimSpace(command[tokenSpans[idx+1].Start:tokenSpans[idx+1].End])
		if rawToken == "" {
			return nil
		}
		if unquoted, ok := shellUnquotePayloadToken(rawToken); ok {
			rawToken = unquoted
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(rawToken), &payload); err != nil {
			return nil
		}
		if payload == nil {
			return nil
		}
		return payload
	}
	return nil
}

func shellUnquotePayloadToken(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if len(token) < 2 {
		return token, false
	}
	switch token[0] {
	case '\'':
		if token[len(token)-1] != '\'' {
			return token, false
		}
		return token[1 : len(token)-1], true
	case '"':
		unquoted, err := strconv.Unquote(token)
		if err == nil {
			return unquoted, true
		}
		if token[len(token)-1] != '"' {
			return token, false
		}
		return token[1 : len(token)-1], true
	default:
		return token, false
	}
}

func reconstructCommandWithPayload(command string, payload map[string]any) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("original command is required")
	}
	if payload == nil {
		return "", fmt.Errorf("payload must be an object")
	}

	tokenSpans := firstSegmentTokenSpans(command)
	for idx := 0; idx < len(tokenSpans)-1; idx++ {
		if strings.TrimSpace(tokenSpans[idx].Text) != "--payload" {
			continue
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("marshal payload: %w", err)
		}
		replacement := shellQuoteToken(string(payloadJSON))
		valueSpan := tokenSpans[idx+1]
		return command[:valueSpan.Start] + replacement + command[valueSpan.End:], nil
	}
	return "", fmt.Errorf("original command does not contain --payload")
}

type shellTokenSpan struct {
	Start int
	End   int
	Text  string
}

func firstSegmentTokenSpans(command string) []shellTokenSpan {
	var (
		spans      []shellTokenSpan
		current    strings.Builder
		tokenStart = -1
		quote      rune
		escaped    bool
	)

	flush := func(end int) {
		if tokenStart < 0 || current.Len() == 0 {
			tokenStart = -1
			current.Reset()
			return
		}
		spans = append(spans, shellTokenSpan{
			Start: tokenStart,
			End:   end,
			Text:  current.String(),
		})
		tokenStart = -1
		current.Reset()
	}

	for idx, r := range command {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case quote == '"':
			if r == '"' {
				quote = 0
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			current.WriteRune(r)
		default:
			switch {
			case r == '|':
				flush(idx)
				return spans
			case r == '\'' || r == '"':
				if tokenStart < 0 {
					tokenStart = idx
				}
				quote = r
			case r == '\\':
				if tokenStart < 0 {
					tokenStart = idx
				}
				escaped = true
			case strings.ContainsRune(" \t\r\n", r):
				flush(idx)
			default:
				if tokenStart < 0 {
					tokenStart = idx
				}
				current.WriteRune(r)
			}
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush(len(command))
	return spans
}

func shellQuoteToken(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
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

func (s *llmRunStream) InjectToolResult(toolID string, text string, isError bool) bool {
	if s == nil {
		return false
	}
	result := ToolExecutionResult{
		Output:   text,
		ExitCode: 0,
	}
	if isError {
		result.Error = "sub_agent_failed"
		result.ExitCode = -1
	}
	if s.activeToolCall != nil && s.activeToolCall.awaitExternalResult && s.activeToolCall.toolID == strings.TrimSpace(toolID) {
		s.activeToolCall.queuedResult = &result
		return true
	}
	for _, invocation := range s.queuedToolCalls {
		if invocation == nil || !invocation.awaitExternalResult || invocation.toolID != strings.TrimSpace(toolID) {
			continue
		}
		invocation.queuedResult = &result
		return true
	}
	return false
}

func (s *llmRunStream) FinalAssistantContent() (string, bool) {
	if s == nil {
		return "", false
	}
	for i := len(s.messages) - 1; i >= 0; i-- {
		msg := s.messages[i]
		if msg.Role != "assistant" {
			continue
		}
		text, _ := msg.Content.(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		return text, true
	}
	return "", false
}

func isPlanTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "plan_add_tasks", "plan_update_task":
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
		s.emitPendingUsageDelta()
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
	if clientVisible, ok := tool.Meta["clientVisible"].(bool); ok && !clientVisible {
		return nil
	}
	if s.engine.frontend == nil {
		return nil
	}
	handler, ok := s.engine.frontend.Handler(toolName)
	if !ok {
		return nil
	}
	toolTimeout := int64(0)
	if budget := NormalizeBudget(s.execCtx.Budget); budget.Tool.TimeoutMs > 0 {
		toolTimeout = int64(budget.Tool.TimeoutMs)
	}
	awaitAsk := handler.BuildInitialAwaitAsk(toolID, s.session.RunID, tool, payload, 0, toolTimeout)
	if s.runControl != nil && awaitAsk != nil {
		s.runControl.ExpectSubmit(awaitingContextFromStreamAsk(awaitAsk))
	}
	return nil
}

func cloneAgentDeltas(input []AgentDelta) []AgentDelta {
	if len(input) == 0 {
		return nil
	}
	out := make([]AgentDelta, 0, len(input))
	for _, delta := range input {
		switch value := delta.(type) {
		case DeltaAwaitAsk:
			cloned := value
			cloned.Questions = append([]any(nil), value.Questions...)
			cloned.Approvals = append([]any(nil), value.Approvals...)
			cloned.Forms = append([]any(nil), value.Forms...)
			out = append(out, cloned)
		default:
			out = append(out, delta)
		}
	}
	return out
}

func awaitingContextFromStreamAsk(awaitAsk *stream.AwaitAsk) AwaitingSubmitContext {
	if awaitAsk == nil {
		return AwaitingSubmitContext{}
	}
	return AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms),
	}
}

func awaitingContextFromDeltaAsk(awaitAsk DeltaAwaitAsk) AwaitingSubmitContext {
	return AwaitingSubmitContext{
		AwaitingID: awaitAsk.AwaitingID,
		Mode:       awaitAsk.Mode,
		ItemCount:  awaitItemCount(awaitAsk.Mode, awaitAsk.Questions, awaitAsk.Approvals, awaitAsk.Forms),
	}
}

func awaitItemCount(mode string, questions []any, approvals []any, forms []any) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return len(questions)
	case "approval":
		return len(approvals)
	case "form":
		return len(forms)
	default:
		return 0
	}
}

func (s *llmRunStream) lookupToolDefinition(toolName string) (api.ToolDetailResponse, bool) {
	if s.checker != nil {
		if tool, ok := s.checker.Tool(toolName); ok {
			return tool, true
		}
	}
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
