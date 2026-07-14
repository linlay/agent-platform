package llm

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

const finalAnswerInstruction = "The tool execution loop has reached its configured step limit. Do not call any more tools. Based only on the conversation and tool results already available, provide the final answer or summary now."

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
		if s.currentTurn.cancel != nil {
			s.currentTurn.cancel()
		}
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
		if err := s.fillNextPendingSource(); err != nil {
			s.closeSteers()
			return err
		}
	}
	return nil
}

func (s *llmRunStream) fillNextPendingSource() error {
	if err := s.handleInterruptIfNeeded(); err != nil || len(s.pending) > 0 {
		return err
	}
	if s.finished {
		return io.EOF
	}
	if s.hitlPendingBatch != nil {
		return s.awaitHITLApprovalBatchAndContinue()
	}
	if s.hitlPendingCall != nil {
		return s.awaitHITLSubmitAndExecute()
	}
	if s.activeToolBatch != nil {
		return s.consumeActiveToolBatch()
	}
	if s.activeToolCall != nil {
		return s.invokeActiveToolCallAndPostHook()
	}
	if len(s.queuedToolCalls) > 0 {
		return s.invokeQueuedToolCallsAndPostHook()
	}
	if s.stopAfterToolBatch {
		s.closeSteersAndFinish()
		return nil
	}
	if s.modelTerminalError != nil {
		err := s.modelTerminalError
		s.modelTerminalError = nil
		return err
	}
	if s.modelCall != nil && s.currentTurn == nil {
		return s.openPendingModelCall()
	}
	if s.currentTurn == nil {
		return s.prepareTurnForPending()
	}
	_, err := s.consumeCurrentTurn()
	if err != nil {
		return s.handleModelAttemptError(err)
	}
	return nil
}

func (s *llmRunStream) invokeActiveToolCallAndPostHook() error {
	toolName := s.activeToolCall.toolName
	toolID := s.activeToolCall.toolID
	if err := s.invokeActiveToolCall(); err != nil {
		return err
	}
	if s.skipPostToolHook {
		s.skipPostToolHook = false
		return nil
	}
	if s.postToolHook != nil && s.postToolHook(toolName, toolID) == PostToolStop {
		s.stopAfterToolBatch = true
	}
	return nil
}

func (s *llmRunStream) prepareTurnForPending() error {
	if s.execCtx != nil && s.execCtx.RunLimitFinalAnswerPending {
		if !s.finalTurnAttempted {
			s.finalTurnAttempted = true
			s.execCtx.RunLimitFinalAnswerPending = false
			s.execCtx.RunLimitFinalAnswerActive = true
			s.prepareFinalAnswerTurn()
			return s.prepareNextTurn()
		}
	}
	if s.execCtx != nil && s.execCtx.RunLimits.MaxToolRounds > 0 && s.execCtx.ToolRounds >= s.execCtx.RunLimits.MaxToolRounds && !s.execCtx.RunLimitFinalAnswerActive {
		s.queueRunLimitFinalAnswer()
		return s.prepareTurnForPending()
	}
	if s.step >= s.maxSteps {
		if !s.finalTurnAttempted {
			s.finalTurnAttempted = true
			s.prepareFinalAnswerTurn()
			return s.prepareNextTurn()
		}
		s.enqueueFallback("Tool execution loop reached the maximum number of steps.")
		s.closeSteersAndFinish()
		return nil
	}
	return s.prepareNextTurn()
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
		s.closeSteersAndFinish()
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
	runSeq := s.runLLMChatCompletionCount + 1
	effectiveToolChoice := effectiveTraceToolChoice(s.toolChoice, s.toolSpecs)
	if err := s.ensureSystemProfileRegistered(preparedRequest, effectiveToolChoice); err != nil {
		return err
	}
	s.pending = append(s.pending, s.buildLLMRequestDelta(preparedRequest, effectiveToolChoice))
	s.resetLastCallUsage()
	s.runLLMChatCompletionCount++
	s.lastCallLLMChatCompletionCount = 1
	s.modelCall = &pendingModelCall{
		prepared:            preparedRequest,
		effectiveToolChoice: effectiveToolChoice,
		runSeq:              runSeq,
		attempt:             1,
		maxAttempts:         s.modelMaxAttempts(),
	}
	s.pending = append(s.pending, s.buildModelRunActivity("waiting", s.modelCall, nil))
	return nil
}

func (s *llmRunStream) openPendingModelCall() error {
	call := s.modelCall
	if call == nil {
		return nil
	}
	requestSentAt := time.Now()
	call.attemptStartedAt = requestSentAt
	trace := s.newChatTrace(call.runSeq, call.prepared, call.effectiveToolChoice)
	if trace != nil {
		trace.markSent(requestSentAt)
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
		modelTimeout:   s.modelStreamIdleTimeout(),
	}, call.prepared)
	if err != nil {
		if trace != nil {
			trace.completeError(err)
		}
		return s.handleModelAttemptError(err)
	}
	if trace != nil {
		trace.markResponseStarted(time.Now())
		turn.trace = trace
	}
	turn.requestSentAt = requestSentAt
	if !call.logicalTurnCounted {
		s.execCtx.ModelCalls++
		s.step++
		call.logicalTurnCounted = true
	}
	s.currentTurn = turn
	s.lastTrace = trace
	return nil
}

func (s *llmRunStream) ensureSystemProfileRegistered(prepared preparedProviderRequest, effectiveToolChoice string) error {
	if s == nil {
		return nil
	}
	if len(firstSystemMessageSnapshot(s.messages)) == 0 && len(s.toolSpecs) == 0 {
		return nil
	}
	cacheKey := s.currentSystemCacheKey()
	if s.session.PendingSystemInitKeys[cacheKey] {
		return fmt.Errorf("system profile not registered on query: chatId=%s runId=%s stage=%s cacheKey=%s", strings.TrimSpace(s.session.ChatID), strings.TrimSpace(s.session.RunID), strings.TrimSpace(s.promptBuildOptions.Stage), cacheKey)
	}
	if len(s.currentSystemRefForCall(prepared, effectiveToolChoice)) > 0 {
		return nil
	}
	if s.session.TeamRuntime != nil && !strings.EqualFold(strings.TrimSpace(effectiveToolChoice), "required") {
		return s.registerCurrentSystemProfile(prepared, effectiveToolChoice)
	}
	return fmt.Errorf("system profile not registered on query: chatId=%s runId=%s stage=%s cacheKey=%s", strings.TrimSpace(s.session.ChatID), strings.TrimSpace(s.session.RunID), strings.TrimSpace(s.promptBuildOptions.Stage), cacheKey)
}

func (s *llmRunStream) registerCurrentSystemProfile(prepared preparedProviderRequest, effectiveToolChoice string) error {
	cacheKey, previous, ok := s.currentSystemSnapshot()
	if !ok {
		return fmt.Errorf("system profile not registered on query: chatId=%s runId=%s stage=%s cacheKey=%s", strings.TrimSpace(s.session.ChatID), strings.TrimSpace(s.session.RunID), strings.TrimSpace(s.promptBuildOptions.Stage), s.currentSystemCacheKey())
	}
	systemMessage := firstSystemMessageSnapshot(s.messages)
	tools := openAIToolSpecsToAny(s.toolSpecs)
	model := s.currentModelSnapshot(prepared)
	requestOptions := requestOptionsFromPreparedBody(prepared.RequestBody)
	profile := map[string]any{
		"agentKey":      strings.TrimSpace(previous.AgentKey),
		"cacheKey":      cacheKey,
		"systemMessage": systemMessage,
		"tools":         tools,
	}
	if len(model) > 0 {
		profile["model"] = model
	}
	if toolChoice := strings.TrimSpace(effectiveToolChoice); toolChoice != "" {
		profile["toolChoice"] = toolChoice
	}
	if len(requestOptions) > 0 {
		profile["requestOptions"] = requestOptions
	}
	profile["fingerprint"] = fingerprintLLMCallProfile(profile)
	agentKey := strings.TrimSpace(profile["agentKey"].(string))
	fingerprint := strings.TrimSpace(profile["fingerprint"].(string))
	if agentKey == "" || cacheKey == "" || fingerprint == "" {
		return fmt.Errorf("system profile registration failed: chatId=%s runId=%s cacheKey=%s", strings.TrimSpace(s.session.ChatID), strings.TrimSpace(s.session.RunID), cacheKey)
	}
	if s.session.SystemInitCache == nil {
		s.session.SystemInitCache = map[string]SystemInitSnapshot{}
	}
	s.session.SystemInitCache[cacheKey] = SystemInitSnapshot{
		AgentKey:       agentKey,
		Fingerprint:    fingerprint,
		SystemMessage:  cloneAnyMapViaJSON(systemMessage),
		Tools:          cloneAnySlice(tools),
		Model:          cloneAnyMapViaJSON(model),
		ToolChoice:     strings.TrimSpace(effectiveToolChoice),
		RequestOptions: cloneAnyMapViaJSON(requestOptions),
	}
	s.systemInitCacheUsed = true
	s.pending = append(s.pending, DeltaSyntheticQuery{
		ChatID: s.session.ChatID,
		Role:   api.QueryRoleSystem,
		System: cloneAnyMapViaJSON(profile),
		Kind:   "system-init",
		Stage:  s.promptBuildOptions.Stage,
		Hidden: true,
	})
	return nil
}

func (s *llmRunStream) prepareFinalAnswerTurn() {
	prompt := finalAnswerInstruction
	if s.execCtx != nil && s.execCtx.RunLimitFinalAnswerActive && strings.TrimSpace(s.execCtx.RunLimits.FinalAnswerPrompt) != "" {
		prompt = s.execCtx.RunLimits.FinalAnswerPrompt
	}
	s.messages = append(s.messages, openAIMessage{
		Role:    "user",
		Content: prompt,
	})
}

func (s *llmRunStream) consumeCurrentTurn() (bool, error) {
	eventName, rawChunk, err := s.readCurrentSSEFrame()
	if err != nil {
		if s.isInterrupted() {
			return false, nil
		}
		if errors.Is(err, io.EOF) {
			if s.currentTurn.finishReason == "" && !s.currentTurn.hasMeaningful {
				if s.currentTurn.trace != nil {
					s.currentTurn.trace.completeError(fmt.Errorf("provider stream ended before first valid event"))
				}
				return false, fmt.Errorf("provider stream ended before first valid event")
			}
			if s.currentTurn.finishReason == "" {
				if s.currentTurn.trace != nil {
					s.currentTurn.trace.completeError(io.ErrUnexpectedEOF)
				}
				return false, io.ErrUnexpectedEOF
			}
			return true, s.finishCurrentTurn()
		}
		if s.currentTurn != nil && s.currentTurn.trace != nil {
			s.currentTurn.trace.completeError(err)
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
	done, consumeErr := s.protocol.ConsumeChunk(s, eventName, rawChunk)
	if consumeErr != nil && s.currentTurn != nil && s.currentTurn.trace != nil {
		s.currentTurn.trace.completeError(consumeErr)
	}
	return done, consumeErr
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
	if turn.cancel != nil {
		turn.cancel()
	}
	s.recordCurrentTurnTiming(time.Now())
	s.modelCall = nil

	toolCalls, err := turn.materializeToolCalls()
	if err != nil {
		if turn.trace != nil {
			turn.trace.completeError(err)
		}
		s.emitPendingUsageDelta()
		s.emitDebugLLMChatDelta(turn.trace)
		s.pending = append(s.pending, DeltaError{Error: NewErrorPayload(
			"missing_tool_call_id",
			err.Error(),
			ErrorScopeModel,
			ErrorCategoryModel,
			nil,
		)})
		s.currentTurn = nil
		s.closeSteersAndFinish()
		return nil
	}
	content := turn.content.String()
	if s.teamRouteRequired() && len(toolCalls) == 0 {
		if turn.trace != nil {
			turn.trace.completeOK(content, turn.reasoning.String(), nil, strings.TrimSpace(turn.finishReason), turn.usage)
		}
		s.emitPendingUsageDelta()
		s.emitDebugLLMChatDelta(turn.trace)
		s.currentTurn = nil
		s.pending = append(s.pending, s.buildModelRunActivity("completed", nil, nil))
		if s.teamRouteCorrections < agentteam.MaxRoutingRetries {
			s.teamRouteCorrections++
			s.messages = append(s.messages, openAIMessage{
				Role:    "user",
				Content: "The previous response did not perform the mandatory Team delegation step. Call agent_delegate now; planning tools alone do not satisfy this requirement, and you must not answer with ordinary text yet.",
			})
			return nil
		}
		s.modelTerminalError = fmt.Errorf("TEAM coordinator did not produce a valid agent_delegate call after one correction")
		return nil
	}
	if s.teamRouteRequired() {
		// A provider may send an explanatory preamble together with a valid
		// routing call. The routing phase is hidden, so retain only the tool call.
		content = ""
	}
	if s.finalTurnAttempted && len(toolCalls) > 0 {
		if strings.TrimSpace(content) != "" {
			msg := s.newAssistantTurnMessage(turn, content, nil)
			s.messages = append(s.messages, msg)
		}
		if turn.trace != nil {
			turn.trace.appendToolCalls(toolCalls)
			turn.trace.completeOK(content, turn.reasoning.String(), toolCalls, strings.TrimSpace(turn.finishReason), turn.usage)
		}
		s.emitPendingUsageDelta()
		s.emitDebugLLMChatDelta(turn.trace)
		s.currentTurn = nil
		if strings.TrimSpace(content) == "" {
			s.enqueueFallback(s.finalAnswerToolCallFallback())
		}
		s.markRunLimitFinalAnswerCompleted()
		s.closeSteersAndFinish()
		return nil
	}
	if content != "" || len(toolCalls) > 0 {
		msg := s.newAssistantTurnMessage(turn, content, toolCalls)
		s.messages = append(s.messages, msg)
	}
	if turn.trace != nil {
		turn.trace.appendToolCalls(toolCalls)
		turn.trace.completeOK(content, turn.reasoning.String(), toolCalls, strings.TrimSpace(turn.finishReason), turn.usage)
	}

	s.emitPendingUsageDelta()
	s.emitDebugLLMChatDelta(turn.trace)
	s.currentTurn = nil

	if len(toolCalls) == 0 {
		s.pending = append(s.pending, s.buildModelRunActivity("completed", nil, nil))
		if s.appendTailSteersBeforeFinish() {
			return nil
		}
		if strings.TrimSpace(content) == "" {
			if s.runLimitFinalAnswerActive() {
				s.enqueueFallback(s.finalAnswerToolCallFallback())
			} else {
				s.enqueueFallback("Model returned no assistant content.")
			}
		}
		if finishReason := strings.TrimSpace(turn.finishReason); finishReason != "" && !strings.EqualFold(finishReason, "tool_calls") {
			s.pending = append(s.pending, DeltaFinishReason{Reason: finishReason})
		}
		s.markRunLimitFinalAnswerCompleted()
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
		s.closeSteersAndFinish()
		return nil
	}

	type preparedTurnToolCall struct {
		toolCall        openAIToolCall
		invocation      *preparedToolInvocation
		immediateEvents []AgentDelta
		toolMessage     *openAIMessage
	}
	toolIDs := make([]string, 0, len(toolCalls))
	fileChanges := map[string]map[string]any{}
	preparedCalls := make([]preparedTurnToolCall, 0, len(toolCalls))
	toolRoundLimited := s.toolRoundLimitReached()
	if toolRoundLimited {
		s.queueRunLimitFinalAnswer()
	}
	willExecuteTool := false
	for _, toolCall := range toolCalls {
		toolIDs = append(toolIDs, toolCall.ID)
		var invocation *preparedToolInvocation
		var immediateEvents []AgentDelta
		var toolMessage *openAIMessage
		if toolRoundLimited {
			invocation, immediateEvents, toolMessage = s.prepareRunLimitToolCall(toolCall)
		} else {
			invocation, immediateEvents, toolMessage = s.prepareToolCall(toolCall)
		}
		if fileChange := s.estimatedToolFileChange(invocation); len(fileChange) > 0 {
			fileChanges[toolCall.ID] = fileChange
		}
		if invocation != nil {
			willExecuteTool = true
		}
		preparedCalls = append(preparedCalls, preparedTurnToolCall{
			toolCall:        toolCall,
			invocation:      invocation,
			immediateEvents: immediateEvents,
			toolMessage:     toolMessage,
		})
	}
	if willExecuteTool && s.execCtx != nil && s.execCtx.RunLimits.MaxToolRounds > 0 {
		s.execCtx.ToolRounds++
	}
	if len(fileChanges) == 0 {
		fileChanges = nil
	}
	s.pending = append(s.pending, DeltaToolEnd{ToolIDs: toolIDs, FileChanges: fileChanges})
	s.pending = append(s.pending, s.buildModelRunActivity("completed", nil, nil))
	for _, prepared := range preparedCalls {
		toolCall := prepared.toolCall
		invocation := prepared.invocation
		immediateEvents := prepared.immediateEvents
		toolMessage := prepared.toolMessage
		if len(immediateEvents) > 0 {
			s.pending = append(s.pending, immediateEvents...)
		}
		if toolMessage != nil {
			s.messages = append(s.messages, *toolMessage)
			if turn.trace != nil {
				turn.trace.appendToolResult(&preparedToolInvocation{
					toolID:   toolCall.ID,
					toolName: toolCall.Function.Name,
				}, traceToolMessageContent(*toolMessage), map[string]any{"content": traceToolMessageContent(*toolMessage)})
			}
		}
		if invocation != nil {
			s.queuedToolCalls = append(s.queuedToolCalls, invocation)
		}
	}
	if s.activeToolCall == nil && len(s.queuedToolCalls) > 0 {
		if s.prepareQueuedBashApprovalBatch() {
			return nil
		}
	}
	s.queuedToolCalls = s.prioritizeAwaitingToolCalls(s.queuedToolCalls)
	return nil
}

func (s *llmRunStream) teamRouteRequired() bool {
	return s != nil && s.session.TeamRuntime != nil && strings.EqualFold(strings.TrimSpace(s.toolChoice), "required")
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
	allowRunLimitFinalAnswer := s.runLimitFinalAnswerActive()
	elapsed := time.Since(s.execCtx.StartedAt) - s.execCtx.BudgetPaused
	if elapsed < 0 {
		elapsed = 0
	}
	if budget.Timeout > 0 && elapsed > budget.RunTimeout() {
		return NewErrorPayload(
			"run_timeout",
			"run exceeded configured timeout",
			ErrorScopeRun,
			ErrorCategoryTimeout,
			map[string]any{
				"elapsedMs": elapsed.Milliseconds(),
				"timeout":   budget.Timeout,
			},
		)
	}
	if budget.MaxSteps > 0 && s.execCtx.ModelCalls >= budget.MaxSteps && !allowRunLimitFinalAnswer {
		return NewErrorPayload(
			"model_calls_exceeded",
			"model step budget exceeded",
			ErrorScopeModel,
			ErrorCategoryModel,
			map[string]any{
				"modelCalls": s.execCtx.ModelCalls,
				"limitValue": budget.MaxSteps,
				"limitName":  "budget.maxSteps",
			},
		)
	}
	if budget.Tool.MaxCalls > 0 && s.execCtx.ToolCalls > budget.Tool.MaxCalls && !allowRunLimitFinalAnswer {
		return NewErrorPayload(
			"tool_calls_exceeded",
			"tool call budget exceeded",
			ErrorScopeTool,
			ErrorCategoryTool,
			map[string]any{
				"toolCalls":  s.execCtx.ToolCalls,
				"limitValue": budget.Tool.MaxCalls,
				"limitName":  "budget.tool.maxCalls",
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

func (s *llmRunStream) AllowOptionalTools() {
	if s == nil {
		return
	}
	s.allowToolUse = true
	s.toolChoice = "auto"
}

func (s *llmRunStream) appendPendingSteers() {
	if s.runControl == nil {
		return
	}
	s.appendSteers(s.runControl.DrainSteers())
}

func (s *llmRunStream) appendTailSteersBeforeFinish() bool {
	if s.runControl == nil {
		return false
	}
	steers := s.runControl.DrainSteersBeforeFinish()
	if len(steers) == 0 {
		return false
	}
	s.appendSteers(steers)
	return true
}

func (s *llmRunStream) appendSteers(steers []api.SteerRequest) {
	for _, steer := range steers {
		s.pending = append(s.pending, NewSteerDelta(steer))
		if strings.TrimSpace(steer.Message) != "" {
			s.pendingSteerInputs = append(s.pendingSteerInputs, map[string]any{
				"role":    "user",
				"content": steer.Message,
			})
		}
		s.messages = append(s.messages, openAIMessage{
			Role:    "user",
			Content: steer.Message,
		})
	}
}

func (s *llmRunStream) closeSteersAndFinish() {
	s.closeSteers()
	s.finished = true
}

func (s *llmRunStream) closeSteers() {
	if s.runControl != nil {
		s.runControl.CloseSteers()
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
		if s.currentTurn.cancel != nil {
			s.currentTurn.cancel()
		}
	}
	if !s.cancelSent {
		s.cancelSent = true
		var trace *llmChatTrace
		if s.currentTurn != nil && s.currentTurn.trace != nil {
			trace = s.currentTurn.trace
			info := InterruptInfo{}
			if s.runControl != nil {
				if snapshot, ok := s.runControl.InterruptInfo(); ok {
					info = snapshot
				}
			}
			trace.completeInterrupted(info)
		}
		s.emitPendingUsageDelta()
		s.emitDebugLLMChatDelta(trace)
		s.currentTurn = nil
		s.activeToolBatch = nil
		s.pending = append(s.pending, DeltaRunCancel{RunID: s.session.RunID})
		return nil
	}
	s.currentTurn = nil
	s.activeToolBatch = nil
	return ErrRunInterrupted
}
