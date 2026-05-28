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
	case PlanningSnapshot:
		return d.handlePlanningSnapshot(value)
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
			"params":     value.Params,
		})}
	case AwaitingAnswer:
		event := newAwaitingAnswerEvent(value)
		if event.Type == "" {
			return nil
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
	case InputDebugPreCall:
		runUsage := map[string]any{
			"promptTokens":           value.RunPromptTokens,
			"completionTokens":       value.RunCompletionTokens,
			"totalTokens":            value.RunTotalTokens,
			"llmChatCompletionCount": value.RunLLMChatCompletionCount,
		}
		addDetailedUsage(runUsage, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens)
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 {
			d.state.runUsage = &runUsageState{
				PromptTokens:           value.RunPromptTokens,
				CompletionTokens:       value.RunCompletionTokens,
				TotalTokens:            value.RunTotalTokens,
				CachedTokens:           value.RunCachedTokens,
				ReasoningTokens:        value.RunReasoningTokens,
				PromptCacheHitTokens:   value.RunPromptCacheHitTokens,
				PromptCacheMissTokens:  value.RunPromptCacheMissTokens,
				LLMChatCompletionCount: value.RunLLMChatCompletionCount,
			}
		}
		payload := map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
			"data": map[string]any{
				"provider": map[string]any{
					"key":      value.ProviderKey,
					"endpoint": value.ProviderEndpoint,
				},
				"model": map[string]any{
					"key": value.ModelKey,
					"id":  value.ModelID,
				},
				"requestBody":    clonePayload(value.RequestBody),
				"injectedPrompt": clonePayload(value.InjectedPrompt),
				"systemRef":      clonePayload(value.SystemRef),
				"contextWindow": map[string]any{
					"maxSize":       value.ContextWindow,
					"actualSize":    value.CurrentContextSize,
					"estimatedSize": value.EstimatedNextCallSize,
				},
				"usage": map[string]any{
					"runUsage": runUsage,
				},
			},
		}
		if value.TaskID != "" {
			payload["taskId"] = value.TaskID
		}
		return []StreamEvent{NewEvent("debug.preCall", payload)}
	case InputDebugPostCall:
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 {
			d.state.runUsage = runUsageStateFromValues(value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount)
		}
		llmReturnUsage := map[string]any{
			"promptTokens":           value.LLMReturnPromptTokens,
			"completionTokens":       value.LLMReturnCompletionTokens,
			"totalTokens":            value.LLMReturnTotalTokens,
			"llmChatCompletionCount": value.LLMReturnLLMChatCompletionCount,
		}
		addDetailedUsage(llmReturnUsage, value.LLMReturnCachedTokens, value.LLMReturnReasoningTokens, value.LLMReturnPromptCacheHitTokens, value.LLMReturnPromptCacheMissTokens)
		runUsage := map[string]any{
			"promptTokens":           value.RunPromptTokens,
			"completionTokens":       value.RunCompletionTokens,
			"totalTokens":            value.RunTotalTokens,
			"llmChatCompletionCount": value.RunLLMChatCompletionCount,
		}
		addDetailedUsage(runUsage, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens)
		payload := map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
			"data": map[string]any{
				"model": map[string]any{
					"key": value.ModelKey,
				},
				"contextWindow": map[string]any{
					"maxSize":       value.ContextWindow,
					"actualSize":    value.CurrentContextSize,
					"estimatedSize": value.EstimatedNextCallSize,
				},
				"usage": map[string]any{
					"llmReturnUsage": llmReturnUsage,
					"runUsage":       runUsage,
				},
			},
		}
		if value.TaskID != "" {
			payload["taskId"] = value.TaskID
		}
		return []StreamEvent{NewEvent("debug.postCall", payload)}
	case InputUsageSnapshot:
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 {
			d.state.runUsage = runUsageStateFromValues(value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount)
		}
		return []StreamEvent{usageSnapshotEvent(d.request.RunID, value.TaskID, value.ChatID, value.ModelKey, value.ContextWindow, value.CurrentContextSize, value.EstimatedNextCallSize, value.CacheDiagnostics, value.LLMReturnPromptTokens, value.LLMReturnCompletionTokens, value.LLMReturnTotalTokens, value.LLMReturnCachedTokens, value.LLMReturnReasoningTokens, value.LLMReturnPromptCacheHitTokens, value.LLMReturnPromptCacheMissTokens, value.LLMReturnLLMChatCompletionCount, value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount)}
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

func usageSnapshotEvent(runID string, taskID string, chatID string, modelKey string, contextWindow int, currentContextSize int, estimatedNextCallSize int, cacheDiagnostics map[string]any, currentPromptTokens int, currentCompletionTokens int, currentTotalTokens int, currentCachedTokens int, currentReasoningTokens int, currentPromptCacheHitTokens int, currentPromptCacheMissTokens int, currentLLMChatCompletionCount int, runPromptTokens int, runCompletionTokens int, runTotalTokens int, runCachedTokens int, runReasoningTokens int, runPromptCacheHitTokens int, runPromptCacheMissTokens int, runLLMChatCompletionCount int) StreamEvent {
	currentUsage := usageMapFromValues(currentPromptTokens, currentCompletionTokens, currentTotalTokens, currentCachedTokens, currentReasoningTokens, currentPromptCacheHitTokens, currentPromptCacheMissTokens, currentLLMChatCompletionCount, false)
	runUsage := usageMapFromValues(runPromptTokens, runCompletionTokens, runTotalTokens, runCachedTokens, runReasoningTokens, runPromptCacheHitTokens, runPromptCacheMissTokens, runLLMChatCompletionCount, true)
	payload := map[string]any{
		"runId":  runID,
		"chatId": chatID,
		"model": map[string]any{
			"key": modelKey,
		},
		"contextWindow": map[string]any{
			"maxSize":               contextWindow,
			"currentSize":           currentContextSize,
			"estimatedNextCallSize": estimatedNextCallSize,
			"actualSize":            currentContextSize,
			"estimatedSize":         estimatedNextCallSize,
		},
		"usage": map[string]any{
			"current": currentUsage,
			"run":     runUsage,
		},
	}
	if taskID != "" {
		payload["taskId"] = taskID
	}
	if len(cacheDiagnostics) > 0 {
		payload["cacheDiagnostics"] = clonePayload(cacheDiagnostics)
	}
	return NewEvent("usage.snapshot", payload)
}

func runUsageStateFromValues(promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int) *runUsageState {
	return &runUsageState{
		PromptTokens:           promptTokens,
		CompletionTokens:       completionTokens,
		TotalTokens:            totalTokens,
		CachedTokens:           cachedTokens,
		ReasoningTokens:        reasoningTokens,
		PromptCacheHitTokens:   promptCacheHitTokens,
		PromptCacheMissTokens:  promptCacheMissTokens,
		LLMChatCompletionCount: llmChatCompletionCount,
	}
}

func usageMapFromValues(promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, includeLLMChatCompletionCount bool) map[string]any {
	out := map[string]any{
		"promptTokens":     promptTokens,
		"completionTokens": completionTokens,
		"totalTokens":      totalTokens,
	}
	if includeLLMChatCompletionCount {
		out["llmChatCompletionCount"] = llmChatCompletionCount
	}
	addDetailedUsage(out, cachedTokens, reasoningTokens, promptCacheHitTokens, promptCacheMissTokens)
	return out
}
