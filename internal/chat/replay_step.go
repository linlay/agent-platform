package chat

import "strings"

func (r *historyReplay) replayStep(line map[string]any) error {
	runID, _ := line["runId"].(string)
	rd := ensureRun(r.runs, &r.runOrder, runID)

	taskID, _ := line["taskId"].(string)
	taskName, _ := line["taskName"].(string)
	taskDescription, _ := line["taskDescription"].(string)
	taskStatus, _ := line["taskStatus"].(string)
	taskAgentKey := lineTaskAgentKey(line)
	taskMainToolID := taskToolIDFromLine(line)

	presentation := stringFromAny(line["presentation"])
	rootContent := false
	if meta, ok := r.taskQueries[replayedTaskQueryKey(runID, taskID)]; ok {
		if strings.TrimSpace(taskName) == "" {
			taskName = meta.TaskName
		}
		if strings.TrimSpace(taskDescription) == "" {
			taskDescription = meta.TaskDesc
		}
		if strings.TrimSpace(taskAgentKey) == "" {
			taskAgentKey = meta.TaskAgentKey
		}
		if strings.TrimSpace(taskMainToolID) == "" {
			taskMainToolID = meta.MainToolID
		}

		presentation = firstNonEmptyReplayString(presentation, meta.Presentation)
		rootContent = meta.RootContent
	}
	ts := int64FromAny(line["updatedAt"])
	if !rootContent {
		if events := beginReplayedSubTask(rd, runID, taskID, taskName, taskDescription, taskAgentKey, taskMainToolID, ts, r.nextSeq); len(events) > 0 {
			if r.isTeamRun(runID) {
				events = decorateReplayedTeamTaskEvents(events, taskAgentKey, presentation)
			}
			rd.events = append(rd.events, events...)
		}
	}
	stepUsage, _ := line["usage"].(map[string]any)
	stepContextWindow, _ := line["contextWindow"].(map[string]any)
	stepContextWindow = contextWindowWithStepModelMetadata(stepContextWindow, line)
	if cw := synthesizedUsageSnapshotContextWindow(stepContextWindow); len(cw) > 0 {
		r.latestContextWindow = cw
	}
	options := replayMessageOptions{}
	if r.isTeamRun(runID) {

		options.Presentation = presentation
		if strings.TrimSpace(taskID) != "" {
			options.ActorType = "agent"
			options.AgentKey = taskAgentKey
			if strings.TrimSpace(options.Presentation) == "" {
				options.Presentation = "task"
			}
		} else {
			options.ActorType = "agent"
			options.AgentKey = r.ownerForRun(runID).AgentKey
			options.Presentation = "reply"
		}
	}
	if err := r.replayStepMessages(rd, line, taskID, options); err != nil {
		return err
	}
	r.accumulateStepUsage(rd, stepUsage)
	r.accumulateToolUsage(rd, line["toolUsage"])
	if events := finishReplayedSubTaskIfTerminal(rd, taskID, taskStatus, ts, r.nextSeq); len(events) > 0 {
		if r.isTeamRun(runID) {
			events = decorateReplayedTeamTaskEvents(events, taskAgentKey, presentation)
		}
		rd.events = append(rd.events, events...)
	}
	return nil
}

// replayStepMessages keeps waiting prompts beside their tool calls and sources
// beside their results; unmatched items are appended at the end of the step.
func (r *historyReplay) replayStepMessages(rd *chatRunData, line map[string]any, taskID string, options replayMessageOptions) error {
	chatID, _ := line["chatId"].(string)
	runID, _ := line["runId"].(string)
	lineLiveSeq := int64FromAny(line["liveSeq"])
	msgs, _ := line["messages"].([]any)
	awaitingReplay, err := newStepAwaitingReplay(line["awaiting"], chatID, runID, lineLiveSeq)
	if err != nil {
		return err
	}
	if state := planningStateFromAwaitingPlanning(line["awaiting"]); state != nil {
		r.planning = state
	}
	sourceReplay := newStepSourceReplay(line["sources"], runID, taskID, lineLiveSeq, r.nextSeq)
	for _, rawMsg := range msgs {
		msgMap, _ := rawMsg.(map[string]any)
		if msgMap == nil {
			continue
		}
		messageEvents, err := storedMessageToEventsWithOptions(msgMap, runID, taskID, lineLiveSeq, r.nextSeq, options)
		if err != nil {
			return err
		}
		for _, ev := range messageEvents {
			rd.events = append(rd.events, ev)
			if ev.Type == "tool.snapshot" {
				rd.events = append(rd.events, awaitingReplay.consumeForTool(ev.String("toolId"))...)
			}
			if ev.Type == "tool.result" {
				rd.events = append(rd.events, sourceReplay.consumeForTool(ev.String("toolId"))...)
			}
		}
	}
	rd.events = append(rd.events, sourceReplay.leftoverEvents()...)
	rd.events = append(rd.events, awaitingReplay.leftoverEvents()...)
	return nil
}

func (r *historyReplay) accumulateStepUsage(rd *chatRunData, stepUsage map[string]any) {
	if hasProviderUsagePayload(stepUsage) {
		stepCacheHitTokens := usageCacheHitTokensFromMap(stepUsage)
		stepCacheMissTokens := usageCacheMissTokensFromMap(stepUsage)
		rd.totalPromptTokens += toIntFromKeys(stepUsage, "promptTokens")
		rd.totalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens")
		rd.totalTotalTokens += toIntFromKeys(stepUsage, "totalTokens")
		rd.totalCachedTokens += stepCacheHitTokens
		rd.totalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "reasoningTokens")
		rd.totalPromptCacheHitTokens += stepCacheHitTokens
		rd.totalPromptCacheMissTokens += stepCacheMissTokens
		rd.totalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount")
		rd.totalToolCallCount += toIntFromKeys(stepUsage, "toolCallCount")
		firstTokenLatencyMs, generationDurationMs := extractStepTiming(stepUsage)
		if firstTokenLatencyMs > 0 {
			rd.totalFirstTokenLatencyMs += firstTokenLatencyMs
			rd.totalFirstTokenLatencyCount++
			r.usage.FirstTokenLatencyTotalMs += firstTokenLatencyMs
			r.usage.FirstTokenLatencyCount++
		}
		if generationDurationMs > 0 {
			rd.totalGenerationDurationMs += generationDurationMs
			r.usage.GenerationDurationMs += generationDurationMs
		}
		r.usage.PromptTokens += toIntFromKeys(stepUsage, "promptTokens")
		r.usage.CompletionTokens += toIntFromKeys(stepUsage, "completionTokens")
		r.usage.TotalTokens += toIntFromKeys(stepUsage, "totalTokens")
		r.usage.CachedTokens += stepCacheHitTokens
		r.usage.ReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "reasoningTokens")
		r.usage.PromptCacheHitTokens += stepCacheHitTokens
		r.usage.PromptCacheMissTokens += stepCacheMissTokens
		r.usage.LlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount")
		r.usage.ToolCallCount += toIntFromKeys(stepUsage, "toolCallCount")
		rd.chatTotalPromptTokens = r.usage.PromptTokens
		rd.chatTotalCompletionTokens = r.usage.CompletionTokens
		rd.chatTotalTotalTokens = r.usage.TotalTokens
		rd.chatTotalCachedTokens = r.usage.CachedTokens
		rd.chatTotalReasoningTokens = r.usage.ReasoningTokens
		rd.chatTotalPromptCacheHitTokens = r.usage.PromptCacheHitTokens
		rd.chatTotalPromptCacheMissTokens = r.usage.PromptCacheMissTokens
		rd.chatTotalLlmChatCompletionCount = r.usage.LlmChatCompletionCount
		rd.chatTotalToolCallCount = r.usage.ToolCallCount
		rd.chatTotalFirstTokenLatencyMs = r.usage.FirstTokenLatencyTotalMs
		rd.chatTotalFirstTokenLatencyCount = r.usage.FirstTokenLatencyCount
		rd.chatTotalGenerationDurationMs = r.usage.GenerationDurationMs
	}
	r.accumulateStepCost(rd, stepUsage)
}

// accumulateToolUsage adds model calls made inside tools. They count toward
// tokens and cost only, never toward model call counts or timing.
func (r *historyReplay) accumulateToolUsage(rd *chatRunData, raw any) {
	entries, _ := raw.([]any)
	for _, rawEntry := range entries {
		entry, _ := rawEntry.(map[string]any)
		if !hasToolBillableUsagePayload(entry) {
			continue
		}
		promptTokens := toIntFromKeys(entry, "promptTokens")
		completionTokens := toIntFromKeys(entry, "completionTokens")
		totalTokens := toIntFromKeys(entry, "totalTokens")
		cacheHitTokens := usageCacheHitTokensFromMap(entry)
		cacheMissTokens := usageCacheMissTokensFromMap(entry)
		reasoningTokens := toNestedIntFromKeys(entry, "completionTokensDetails", "reasoningTokens")
		rd.totalPromptTokens += promptTokens
		rd.totalCompletionTokens += completionTokens
		rd.totalTotalTokens += totalTokens
		rd.totalCachedTokens += cacheHitTokens
		rd.totalReasoningTokens += reasoningTokens
		rd.totalPromptCacheHitTokens += cacheHitTokens
		rd.totalPromptCacheMissTokens += cacheMissTokens
		r.usage.PromptTokens += promptTokens
		r.usage.CompletionTokens += completionTokens
		r.usage.TotalTokens += totalTokens
		r.usage.CachedTokens += cacheHitTokens
		r.usage.ReasoningTokens += reasoningTokens
		r.usage.PromptCacheHitTokens += cacheHitTokens
		r.usage.PromptCacheMissTokens += cacheMissTokens
		r.accumulateStepCost(rd, entry)
	}
}

func (r *historyReplay) accumulateStepCost(rd *chatRunData, stepUsage map[string]any) {
	currency, inputHit, inputMiss, output, total := extractStepCost(stepUsage)
	if currency != "" {
		if rd.estimatedCostCurrency == "" {
			rd.estimatedCostCurrency = currency
		}
		rd.estimatedCostInputHit += inputHit
		rd.estimatedCostInputMiss += inputMiss
		rd.estimatedCostOutput += output
		rd.estimatedCostTotal += total
		if r.usage.EstimatedCostCurrency == "" {
			r.usage.EstimatedCostCurrency = currency
		}
		r.usage.EstimatedCostInputHit += inputHit
		r.usage.EstimatedCostInputMiss += inputMiss
		r.usage.EstimatedCostOutput += output
		r.usage.EstimatedCostTotal += total
	}
}
