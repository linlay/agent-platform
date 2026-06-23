package stream

type StreamEventDispatcher struct {
	request StreamRequest
	state   *StreamEventStateData
}

func NewDispatcher(request StreamRequest) *StreamEventDispatcher {
	return &StreamEventDispatcher{
		request: request,
		state:   NewStateData(),
	}
}

func (d *StreamEventDispatcher) Dispatch(input StreamInput) []StreamEvent {
	if d.state.terminated {
		return nil
	}

	switch value := input.(type) {
	case ReasoningDelta:
		return d.handleReasoningDelta(value)
	case ContentDelta:
		return d.handleContentDelta(value)
	case ToolArgs:
		return d.handleToolArgs(value)
	case ToolEnd:
		return d.handleToolEnd(value)
	case ToolResult:
		return d.handleToolResult(value)
	case ActionArgs:
		return d.handleActionArgs(value)
	case ActionEnd:
		return d.handleActionEnd(value)
	case ActionResult:
		return d.handleActionResult(value)
	case StageMarker:
		return nil
	case PlanUpdate:
		return d.handlePlanUpdate(value)
	case PlanningStart:
		return d.handlePlanningStart(value)
	case PlanningDelta:
		return d.handlePlanningDelta(value)
	case PlanningEnd:
		return d.handlePlanningEnd(value)
	case TaskStart:
		return d.handleTaskStart(value)
	case TaskComplete:
		return d.handleTaskComplete(value)
	case TaskCancel:
		return d.handleTaskCancel(value)
	case TaskError:
		return d.handleTaskError(value)
	case ArtifactPublish:
		artifactCount := value.ArtifactCount
		if artifactCount <= 0 {
			artifactCount = len(value.Artifacts)
		}
		artifacts := append([]map[string]any(nil), value.Artifacts...)
		return []StreamEvent{NewEvent("artifact.publish", map[string]any{
			"chatId":        value.ChatID,
			"runId":         value.RunID,
			"artifactCount": artifactCount,
			"artifacts":     artifacts,
		})}
	case SourcePublish:
		return d.handleSourcePublish(value)
	case AwaitAsk:
		event := d.newAwaitAskEvent(value)
		if event.Type == "" {
			return nil
		}
		return []StreamEvent{event}
	case RequestSubmit:
		return []StreamEvent{NewEvent("request.submit", map[string]any{
			"requestId":  value.RequestID,
			"chatId":     value.ChatID,
			"runId":      value.RunID,
			"awaitingId": value.AwaitingID,
			"submitId":   value.SubmitID,
			"params":     value.Params,
		})}
	case AwaitingAnswer:
		event := newAwaitingAnswerEvent(value)
		if event.Type == "" {
			return nil
		}
		if startedAt, ok := d.state.awaitingAskAtByID[value.AwaitingID]; ok {
			event.Payload["durationMs"] = nonNegativeDurationMs(startedAt, event.Timestamp)
			delete(d.state.awaitingAskAtByID, value.AwaitingID)
		}
		return []StreamEvent{event}
	case RequestSteer:
		events := d.closeOpenBlocks()
		events = append(events, NewEvent("request.steer", map[string]any{
			"requestId": value.RequestID,
			"chatId":    value.ChatID,
			"runId":     value.RunID,
			"steerId":   value.SteerID,
			"message":   value.Message,
			"role":      "user",
		}))
		return events
	case RunCancel:
		events := d.closeOpenBlocks()
		payload := map[string]any{"runId": value.RunID}
		if usage := d.usagePayload(); usage != nil {
			payload["usage"] = usage
		}
		events = append(events, NewEvent("run.cancel", payload))
		d.state.terminated = true
		return events
	case InputDebugLLMChat:
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 || value.RunToolCallCount > 0 {
			d.state.runUsage = runUsageStateFromValues(value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount, value.RunToolCallCount)
		}
		llmReturnUsage := map[string]any{
			"promptTokens":           value.LLMReturnPromptTokens,
			"completionTokens":       value.LLMReturnCompletionTokens,
			"totalTokens":            value.LLMReturnTotalTokens,
			"llmChatCompletionCount": value.LLMReturnLLMChatCompletionCount,
			"toolCallCount":          value.LLMReturnToolCallCount,
		}
		if value.ModelKey != "" {
			llmReturnUsage["modelKey"] = value.ModelKey
		}
		if value.ReasoningEffort != "" {
			llmReturnUsage["reasoningEffort"] = value.ReasoningEffort
		}
		addDetailedUsage(llmReturnUsage, value.LLMReturnReasoningTokens, value.LLMReturnPromptCacheHitTokens, value.LLMReturnPromptCacheMissTokens)
		runUsage := map[string]any{
			"promptTokens":           value.RunPromptTokens,
			"completionTokens":       value.RunCompletionTokens,
			"totalTokens":            value.RunTotalTokens,
			"llmChatCompletionCount": value.RunLLMChatCompletionCount,
			"toolCallCount":          value.RunToolCallCount,
		}
		addDetailedUsage(runUsage, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens)
		data := map[string]any{
			"provider": map[string]any{
				"key":      value.ProviderKey,
				"endpoint": value.ProviderEndpoint,
			},
			"model": map[string]any{
				"key":             value.ModelKey,
				"id":              value.ModelID,
				"reasoningEffort": value.ReasoningEffort,
			},
			"contextWindow": map[string]any{
				"maxSize":               value.ContextWindow,
				"currentSize":           value.CurrentContextSize,
				"estimatedNextCallSize": value.EstimatedNextCallSize,
			},
			"usage": map[string]any{
				"llmReturnUsage": llmReturnUsage,
				"runUsage":       runUsage,
			},
			"status": value.Status,
			"runSeq": value.RunSeq,
		}
		if value.TraceFile != "" || value.TraceURL != "" {
			data["trace"] = map[string]any{
				"file": value.TraceFile,
				"url":  value.TraceURL,
			}
		}
		if len(value.SystemRef) > 0 {
			data["systemRef"] = clonePayload(value.SystemRef)
		}
		payload := map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
			"data":   data,
		}
		if value.TaskID != "" {
			payload["taskId"] = value.TaskID
		}
		return []StreamEvent{NewEvent("debug.llmChat", payload)}
	case InputUsageSnapshot:
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 || value.RunToolCallCount > 0 {
			d.state.runUsage = runUsageStateFromValues(value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount, value.RunToolCallCount)
		}
		return []StreamEvent{usageSnapshotEvent(d.request.RunID, value.TaskID, value.ChatID, value.ModelKey, value.ReasoningEffort, value.ContextWindow, value.CurrentContextSize, value.EstimatedNextCallSize, value.LLMReturnPromptTokens, value.LLMReturnCompletionTokens, value.LLMReturnTotalTokens, value.LLMReturnCachedTokens, value.LLMReturnReasoningTokens, value.LLMReturnPromptCacheHitTokens, value.LLMReturnPromptCacheMissTokens, value.LLMReturnLLMChatCompletionCount, value.LLMReturnToolCallCount, value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount, value.RunToolCallCount)}
	case InputRunComplete:
		d.state.runFinishReason = value.FinishReason
		return nil
	case InputRunError:
		events := d.closeOpenBlocks()
		payload := map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(value.Error, "run_error", "run", "runtime"),
		}
		if usage := d.usagePayload(); usage != nil {
			payload["usage"] = usage
		}
		events = append(events, NewEvent("run.error", payload))
		d.state.terminated = true
		return events
	default:
		return nil
	}
}

func usageSnapshotEvent(runID string, taskID string, chatID string, modelKey string, reasoningEffort string, contextWindow int, currentContextSize int, estimatedNextCallSize int, currentPromptTokens int, currentCompletionTokens int, currentTotalTokens int, currentCachedTokens int, currentReasoningTokens int, currentPromptCacheHitTokens int, currentPromptCacheMissTokens int, currentLLMChatCompletionCount int, currentToolCallCount int, runPromptTokens int, runCompletionTokens int, runTotalTokens int, runCachedTokens int, runReasoningTokens int, runPromptCacheHitTokens int, runPromptCacheMissTokens int, runLLMChatCompletionCount int, runToolCallCount int) StreamEvent {
	currentUsage := usageMapFromValues(currentPromptTokens, currentCompletionTokens, currentTotalTokens, currentCachedTokens, currentReasoningTokens, currentPromptCacheHitTokens, currentPromptCacheMissTokens, currentLLMChatCompletionCount, currentToolCallCount, false)
	runUsage := usageMapFromValues(runPromptTokens, runCompletionTokens, runTotalTokens, runCachedTokens, runReasoningTokens, runPromptCacheHitTokens, runPromptCacheMissTokens, runLLMChatCompletionCount, runToolCallCount, true)
	if modelKey != "" {
		currentUsage["modelKey"] = modelKey
	}
	if reasoningEffort != "" {
		currentUsage["reasoningEffort"] = reasoningEffort
	}
	contextWindowPayload := map[string]any{
		"maxSize":               contextWindow,
		"currentSize":           currentContextSize,
		"estimatedNextCallSize": estimatedNextCallSize,
	}
	payload := map[string]any{
		"runId":         runID,
		"chatId":        chatID,
		"contextWindow": contextWindowPayload,
		"usage": map[string]any{
			"current": currentUsage,
			"run":     runUsage,
		},
	}
	if taskID != "" {
		payload["taskId"] = taskID
	}
	return NewEvent("usage.snapshot", payload)
}

func runUsageStateFromValues(promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, toolCallCount int) *runUsageState {
	return &runUsageState{
		PromptTokens:           promptTokens,
		CompletionTokens:       completionTokens,
		TotalTokens:            totalTokens,
		CachedTokens:           cachedTokens,
		ReasoningTokens:        reasoningTokens,
		PromptCacheHitTokens:   promptCacheHitTokens,
		PromptCacheMissTokens:  promptCacheMissTokens,
		LLMChatCompletionCount: llmChatCompletionCount,
		ToolCallCount:          toolCallCount,
	}
}

func usageMapFromValues(promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, toolCallCount int, includeLLMChatCompletionCount bool) map[string]any {
	out := map[string]any{
		"promptTokens":     promptTokens,
		"completionTokens": completionTokens,
		"totalTokens":      totalTokens,
	}
	if includeLLMChatCompletionCount {
		out["llmChatCompletionCount"] = llmChatCompletionCount
	}
	if toolCallCount > 0 {
		out["toolCallCount"] = toolCallCount
	}
	addDetailedUsage(out, reasoningTokens, promptCacheHitTokens, promptCacheMissTokens)
	return out
}
