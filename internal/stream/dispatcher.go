package stream

import (
	"net/url"
	"strings"
)

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
	case StageMarker:
		return nil
	case ModelTurnCommit:
		return nil
	case ModelTurnDiscard:
		return d.handleModelTurnDiscard(value)
	case SyntheticQuery:
		events := d.closeOpenBlocks()
		events = append(events, NewEvent("request.query", syntheticQueryPayload(d.request, value)))
		return events
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
		artifacts := publicArtifactItems(value.Artifacts, d.request.ChatID)
		artifactCount := len(artifacts)
		if artifactCount == 0 {
			return nil
		}
		payload := map[string]any{
			"chatId":        value.ChatID,
			"runId":         value.RunID,
			"toolId":        value.ToolID,
			"artifactCount": artifactCount,
			"artifacts":     artifacts,
		}
		if value.TaskID != "" {
			payload["taskId"] = value.TaskID
		}
		return []StreamEvent{NewEvent("artifact.publish", payload)}
	case SourcePublish:
		return d.handleSourcePublish(value)
	case AwaitAsk:
		event := d.newAwaitAskEvent(value)
		if event.Type == "" {
			return nil
		}
		return []StreamEvent{event}
	case RequestSubmit:
		payload := map[string]any{
			"requestId":  value.RequestID,
			"chatId":     value.ChatID,
			"runId":      value.RunID,
			"awaitingId": value.AwaitingID,
			"submitId":   value.SubmitID,
			"params":     value.Params,
		}
		if taskID := strings.TrimSpace(value.TaskID); taskID != "" {
			payload["taskId"] = taskID
		}
		return []StreamEvent{NewEvent("request.submit", payload)}
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
	case InputLLMRequest:
		payload := map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
		}
		if value.TaskID != "" {
			payload["taskId"] = value.TaskID
		}
		if value.ActorType != "" {
			payload["actorType"] = value.ActorType
		}
		if value.TeamID != "" {
			payload["teamId"] = value.TeamID
		}
		if value.AgentKey != "" {
			payload["agentKey"] = value.AgentKey
		}
		if value.Presentation != "" {
			payload["presentation"] = value.Presentation
		}
		if len(value.Model) > 0 {
			payload["model"] = clonePayload(value.Model)
		} else if value.ModelKey != "" || value.ReasoningEffort != "" {
			model := map[string]any{}
			if value.ModelKey != "" {
				model["key"] = value.ModelKey
			}
			if value.ReasoningEffort != "" {
				model["reasoningEffort"] = value.ReasoningEffort
			}
			payload["model"] = model
		}
		if len(value.System) > 0 {
			payload["system"] = clonePayload(value.System)
		}
		if len(value.SystemRef) > 0 {
			payload["systemRef"] = clonePayload(value.SystemRef)
		}
		if value.ToolChoice != "" {
			payload["toolChoice"] = value.ToolChoice
		}
		if len(value.RequestOptions) > 0 {
			payload["requestOptions"] = clonePayload(value.RequestOptions)
		}
		if len(value.InputMessages) > 0 {
			payload["inputMessages"] = cloneMessagePayloads(value.InputMessages)
		}
		return []StreamEvent{NewEvent("llm.request", payload)}
	case InputDebugLLMChat:
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 || value.RunToolCallCount > 0 || value.RunFirstTokenLatencyCount > 0 || value.RunGenerationDurationMs > 0 {
			d.state.runUsage = runUsageStateFromValues(value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount, value.RunToolCallCount, value.RunFirstTokenLatencyTotalMs, value.RunFirstTokenLatencyCount, value.RunGenerationDurationMs)
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
		addSingleTimingUsage(llmReturnUsage, value.LLMReturnFirstTokenLatencyMs, value.LLMReturnGenerationDurationMs)
		runUsage := map[string]any{
			"promptTokens":           value.RunPromptTokens,
			"completionTokens":       value.RunCompletionTokens,
			"totalTokens":            value.RunTotalTokens,
			"llmChatCompletionCount": value.RunLLMChatCompletionCount,
			"toolCallCount":          value.RunToolCallCount,
		}
		addDetailedUsage(runUsage, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens)
		addCumulativeTimingUsage(runUsage, value.RunFirstTokenLatencyTotalMs, value.RunFirstTokenLatencyCount, value.RunGenerationDurationMs)
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
		if value.RunTotalTokens > 0 || value.RunLLMChatCompletionCount > 0 || value.RunToolCallCount > 0 || value.RunFirstTokenLatencyCount > 0 || value.RunGenerationDurationMs > 0 {
			d.state.runUsage = runUsageStateFromValues(value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount, value.RunToolCallCount, value.RunFirstTokenLatencyTotalMs, value.RunFirstTokenLatencyCount, value.RunGenerationDurationMs)
		}
		return []StreamEvent{usageSnapshotEvent(d.request.RunID, value.TaskID, value.ChatID, value.ModelKey, value.ReasoningEffort, value.ContextWindow, value.CurrentContextSize, value.EstimatedNextCallSize, value.LLMReturnPromptTokens, value.LLMReturnCompletionTokens, value.LLMReturnTotalTokens, value.LLMReturnCachedTokens, value.LLMReturnReasoningTokens, value.LLMReturnPromptCacheHitTokens, value.LLMReturnPromptCacheMissTokens, value.LLMReturnLLMChatCompletionCount, value.LLMReturnToolCallCount, value.LLMReturnFirstTokenLatencyMs, value.LLMReturnGenerationDurationMs, value.RunPromptTokens, value.RunCompletionTokens, value.RunTotalTokens, value.RunCachedTokens, value.RunReasoningTokens, value.RunPromptCacheHitTokens, value.RunPromptCacheMissTokens, value.RunLLMChatCompletionCount, value.RunToolCallCount, value.RunFirstTokenLatencyTotalMs, value.RunFirstTokenLatencyCount, value.RunGenerationDurationMs)}
	case InputRunActivity:
		payload := map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
			"phase":  value.Phase,
			"status": value.Status,
		}
		if value.TaskID != "" {
			payload["taskId"] = value.TaskID
		}
		if value.Backend != "" {
			payload["backend"] = value.Backend
		}
		if value.Key != "" {
			payload["key"] = value.Key
		}
		if value.Message != "" {
			payload["message"] = value.Message
		}
		if len(value.Retry) > 0 {
			payload["retry"] = clonePayload(value.Retry)
		}
		if len(value.Recovery) > 0 {
			payload["recovery"] = clonePayload(value.Recovery)
		}
		if len(value.Degradation) > 0 {
			payload["degradation"] = clonePayload(value.Degradation)
		}
		return []StreamEvent{NewEvent("run.activity", payload)}
	case InputContextCompact:
		status := strings.ToLower(strings.TrimSpace(value.Status))
		if status != "start" && status != "complete" && status != "failed" {
			return nil
		}
		payload := map[string]any{
			"runId":     value.RunID,
			"chatId":    value.ChatID,
			"compactId": value.CompactID,
			"requestId": value.RequestID,
			"trigger":   value.Trigger,
			"level":     value.Level,
			"scope":     value.Scope,
		}
		if value.SummarySource != "" {
			payload["summarySource"] = value.SummarySource
		}
		if value.PreCompactEstimatedTokens > 0 {
			payload["preCompactEstimatedTokens"] = value.PreCompactEstimatedTokens
		}
		if value.PostCompactEstimatedTokens > 0 {
			payload["postCompactEstimatedTokens"] = value.PostCompactEstimatedTokens
		}
		if value.CompressionRatio > 0 {
			payload["compressionRatio"] = value.CompressionRatio
		}
		if value.RemainingRatio > 0 {
			payload["remainingRatio"] = value.RemainingRatio
		}
		if value.ReleasedRatio > 0 {
			payload["releasedRatio"] = value.ReleasedRatio
		}
		if value.TokensFreed > 0 {
			payload["tokensFreed"] = value.TokensFreed
		}
		if value.ToolsCleared > 0 {
			payload["toolsCleared"] = value.ToolsCleared
		}
		if value.ToolsKept > 0 {
			payload["toolsKept"] = value.ToolsKept
		}
		if len(value.CompactionUsage) > 0 {
			payload["compactionUsage"] = clonePayload(value.CompactionUsage)
		}
		if value.Detail != "" {
			payload["detail"] = value.Detail
		}
		if value.Retryable {
			payload["retryable"] = true
		}
		if len(value.CheckpointMessages) > 0 {
			payload["checkpointMessages"] = cloneMessagePayloads(value.CheckpointMessages)
		}
		if value.PreviousRunState != "" {
			payload["previousRunState"] = value.PreviousRunState
		}
		if value.AwaitingID != "" {
			payload["awaitingId"] = value.AwaitingID
		}
		return []StreamEvent{NewEvent("context.compact."+status, payload)}
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

func publicArtifactItems(items []map[string]any, chatID string) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		resourceURL, _ := item["url"].(string)
		if !validChatScopeURL(resourceURL, chatID) {
			continue
		}
		cloned := clonePayload(item)
		result = append(result, cloned)
	}
	return result
}

func validChatScopeURL(value string, chatID string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsAny(value, "?#") || strings.Contains(value, "//") {
		return false
	}
	if strings.Contains(value, ":") {
		return false
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") {
			return false
		}
		if index == 0 && strings.TrimSpace(chatID) != "" && decoded == strings.TrimSpace(chatID) {
			return false
		}
	}
	return true
}

func usageSnapshotEvent(runID string, taskID string, chatID string, modelKey string, reasoningEffort string, contextWindow int, currentContextSize int, estimatedNextCallSize int, currentPromptTokens int, currentCompletionTokens int, currentTotalTokens int, currentCachedTokens int, currentReasoningTokens int, currentPromptCacheHitTokens int, currentPromptCacheMissTokens int, currentLLMChatCompletionCount int, currentToolCallCount int, currentFirstTokenLatencyMs int64, currentGenerationDurationMs int64, runPromptTokens int, runCompletionTokens int, runTotalTokens int, runCachedTokens int, runReasoningTokens int, runPromptCacheHitTokens int, runPromptCacheMissTokens int, runLLMChatCompletionCount int, runToolCallCount int, runFirstTokenLatencyTotalMs int64, runFirstTokenLatencyCount int, runGenerationDurationMs int64) StreamEvent {
	currentUsage := usageMapFromValues(currentPromptTokens, currentCompletionTokens, currentTotalTokens, currentCachedTokens, currentReasoningTokens, currentPromptCacheHitTokens, currentPromptCacheMissTokens, currentLLMChatCompletionCount, currentToolCallCount, currentFirstTokenLatencyMs, 0, currentGenerationDurationMs, false)
	runUsage := usageMapFromValues(runPromptTokens, runCompletionTokens, runTotalTokens, runCachedTokens, runReasoningTokens, runPromptCacheHitTokens, runPromptCacheMissTokens, runLLMChatCompletionCount, runToolCallCount, runFirstTokenLatencyTotalMs, runFirstTokenLatencyCount, runGenerationDurationMs, true)
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
	if modelKey != "" {
		contextWindowPayload["modelKey"] = modelKey
	}
	if reasoningEffort != "" {
		contextWindowPayload["reasoningEffort"] = reasoningEffort
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

func cloneMessagePayloads(messages []map[string]any) []any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, clonePayload(msg))
	}
	return out
}

func runUsageStateFromValues(promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, toolCallCount int, firstTokenLatencyTotalMs int64, firstTokenLatencyCount int, generationDurationMs int64) *runUsageState {
	return &runUsageState{
		PromptTokens:             promptTokens,
		CompletionTokens:         completionTokens,
		TotalTokens:              totalTokens,
		CachedTokens:             cachedTokens,
		ReasoningTokens:          reasoningTokens,
		PromptCacheHitTokens:     promptCacheHitTokens,
		PromptCacheMissTokens:    promptCacheMissTokens,
		LLMChatCompletionCount:   llmChatCompletionCount,
		ToolCallCount:            toolCallCount,
		FirstTokenLatencyTotalMs: firstTokenLatencyTotalMs,
		FirstTokenLatencyCount:   firstTokenLatencyCount,
		GenerationDurationMs:     generationDurationMs,
	}
}

func usageMapFromValues(promptTokens int, completionTokens int, totalTokens int, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int, llmChatCompletionCount int, toolCallCount int, firstTokenLatencyTotalMs int64, firstTokenLatencyCount int, generationDurationMs int64, includeLLMChatCompletionCount bool) map[string]any {
	out := map[string]any{
		"promptTokens":     promptTokens,
		"completionTokens": completionTokens,
		"totalTokens":      totalTokens,
	}
	if includeLLMChatCompletionCount {
		out["llmChatCompletionCount"] = llmChatCompletionCount
	}
	out["toolCallCount"] = toolCallCount
	addDetailedUsage(out, reasoningTokens, promptCacheHitTokens, promptCacheMissTokens)
	if includeLLMChatCompletionCount {
		addCumulativeTimingUsage(out, firstTokenLatencyTotalMs, firstTokenLatencyCount, generationDurationMs)
	} else {
		addSingleTimingUsage(out, firstTokenLatencyTotalMs, generationDurationMs)
	}
	return out
}
