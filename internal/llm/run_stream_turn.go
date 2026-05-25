package llm

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
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
				s.stopAfterToolBatch = true
			}
			continue
		}
		if len(s.queuedToolCalls) > 0 {
			s.activateNextToolCall()
			continue
		}
		if s.stopAfterToolBatch {
			s.finished = true
			continue
		}
		if s.currentTurn == nil {
			if s.step >= s.maxSteps {
				if !s.finalTurnAttempted {
					s.finalTurnAttempted = true
					s.prepareFinalTurnWithoutTools()
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
	s.runLLMChatCompletionCount++
	s.lastCallLLMChatCompletionCount = 1
	s.pending = append(s.pending, DeltaDebugPreCall{
		ChatID:                    s.session.ChatID,
		ProviderKey:               s.provider.Key,
		ProviderEndpoint:          preparedRequest.Endpoint,
		ModelKey:                  s.model.Key,
		ModelID:                   s.model.ModelID,
		RequestBody:               preparedRequest.RequestBody,
		InjectedPrompt:            buildInjectedPromptPayload(s.session, s.req, s.promptBuildOptions, s.messages),
		SystemRef:                 s.currentSystemRef(),
		ContextWindow:             s.effectiveContextWindow(),
		CurrentContextSize:        s.currentContextSize(),
		EstimatedNextCallSize:     s.estimatedNextCallSize(),
		RunPromptTokens:           s.runPromptTokens,
		RunCompletionTokens:       s.runCompletionTokens,
		RunTotalTokens:            s.runTotalTokens,
		RunCachedTokens:           s.runCachedTokens,
		RunReasoningTokens:        s.runReasoningTokens,
		RunPromptCacheHitTokens:   s.runPromptCacheHitTokens,
		RunPromptCacheMissTokens:  s.runPromptCacheMissTokens,
		RunLLMChatCompletionCount: s.runLLMChatCompletionCount,
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

func (s *llmRunStream) prepareFinalTurnWithoutTools() {
	s.toolSpecs = nil
	s.promptBuildOptions.ToolDefinitions = nil
	s.promptBuildOptions.IncludeAfterCallHints = false
	systemPrompt := strings.TrimSpace(s.finalTurnSystem)
	if systemPrompt == "" {
		systemPrompt = buildSystemPrompt(s.session, s.req, s.model.Key, PromptBuildOptions{
			Stage:                 s.promptBuildOptions.Stage,
			ToolDefinitions:       nil,
			IncludeAfterCallHints: false,
		})
	}
	if strings.TrimSpace(systemPrompt) != "" {
		s.messages = replaceSystemMessage(s.messages, openAIMessage{Role: "system", Content: systemPrompt})
	}
}

func deriveFinalTurnSystemPrompt(messages []openAIMessage, session QuerySession, req api.QueryRequest, modelKey string, options PromptBuildOptions) string {
	systemPrompt := firstSystemPromptContent(messages)
	if stripped, ok := stripToolAppendixFromSystemPrompt(systemPrompt, session.PromptAppend, options.ToolDefinitions, options.IncludeAfterCallHints); ok {
		return stripped
	}
	return buildSystemPrompt(session, req, modelKey, PromptBuildOptions{
		Stage:                 options.Stage,
		ToolDefinitions:       nil,
		IncludeAfterCallHints: false,
	})
}

func firstSystemPromptContent(messages []openAIMessage) string {
	for _, msg := range messages {
		if strings.TrimSpace(msg.Role) != "system" {
			continue
		}
		content, _ := msg.Content.(string)
		return content
	}
	return ""
}

func stripToolAppendixFromSystemPrompt(systemPrompt string, appendConfig PromptAppendConfig, toolDefs []api.ToolDetailResponse, includeAfterCallHints bool) (string, bool) {
	prompt := strings.TrimSpace(systemPrompt)
	if prompt == "" {
		return "", false
	}
	appendix := strings.TrimSpace(buildToolAppendix(toolDefs, appendConfig, includeAfterCallHints))
	if appendix == "" {
		return prompt, true
	}
	if prompt == appendix {
		return "", true
	}
	suffix := "\n\n" + appendix
	if !strings.HasSuffix(prompt, suffix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimSuffix(prompt, suffix)), true
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

func (s *llmRunStream) finishCurrentTurn() error {
	if s.currentTurn != nil && s.responseReasoningFormat() == "THINK_TAG_CONTENT" {
		s.appendThinkTagContent("", true)
	}
	turn := s.currentTurn
	if turn == nil {
		return nil
	}
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
		s.currentTurn = nil
		s.finished = true
		return nil
	}
	content := turn.content.String()
	if content != "" || len(toolCalls) > 0 {
		msg := s.newAssistantTurnMessage(turn, content, toolCalls)
		s.messages = append(s.messages, msg)
	}

	s.emitPendingUsageDelta()
	s.currentTurn = nil

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

func (s *llmRunStream) newAssistantTurnMessage(turn *providerTurnStream, content string, toolCalls []openAIToolCall) openAIMessage {
	msg := openAIMessage{Role: "assistant"}
	if content != "" {
		msg.Content = content
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if turn != nil && preserveReasoningContent(s.protocolConfig, s.stageSettings) {
		msg.ReasoningContent = turn.reasoning.String()
	}
	return msg
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
	}
	if !s.cancelSent {
		s.cancelSent = true
		s.emitPendingUsageDelta()
		s.currentTurn = nil
		s.pending = append(s.pending, DeltaRunCancel{RunID: s.session.RunID})
		return nil
	}
	s.currentTurn = nil
	return ErrRunInterrupted
}
