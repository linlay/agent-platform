package chat

import "strings"

func (r *historyReplay) replayStep(line map[string]any) error {
	runID, _ := line["runId"].(string)
	rd := ensureRun(r.runs, &r.runOrder, runID)

	stage, _ := line["stage"].(string)
	taskID, _ := line["taskId"].(string)
	taskName, _ := line["taskName"].(string)
	taskDescription, _ := line["taskDescription"].(string)
	taskStatus, _ := line["taskStatus"].(string)
	taskSubAgentKey, _ := line["taskSubAgentKey"].(string)
	taskMainToolID := taskToolIDFromLine(line)
	teamID := stringFromAny(line["teamId"])
	presentation := stringFromAny(line["presentation"])
	rootContent := false
	if meta, ok := r.taskQueries[replayedTaskQueryKey(runID, taskID)]; ok {
		if strings.TrimSpace(taskName) == "" {
			taskName = meta.TaskName
		}
		if strings.TrimSpace(taskDescription) == "" {
			taskDescription = meta.TaskDesc
		}
		if strings.TrimSpace(taskSubAgentKey) == "" {
			taskSubAgentKey = meta.SubAgentKey
		}
		if strings.TrimSpace(taskMainToolID) == "" {
			taskMainToolID = meta.MainToolID
		}
		teamID = firstNonEmptyReplayString(teamID, meta.TeamID)
		presentation = firstNonEmptyReplayString(presentation, meta.Presentation)
		rootContent = meta.RootContent
	}
	ts := int64FromAny(line["updatedAt"])
	if !rootContent {
		if events := beginReplayedSubTask(rd, runID, taskID, taskName, taskDescription, taskSubAgentKey, taskMainToolID, ts, r.nextSeq); len(events) > 0 {
			if r.orchestratedTeam {
				events = decorateReplayedTeamTaskEvents(events, firstNonEmptyReplayString(teamID, r.summary.TeamID), taskSubAgentKey, presentation)
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
	if r.orchestratedTeam {
		options.TeamID = firstNonEmptyReplayString(teamID, r.summary.TeamID)
		options.Presentation = presentation
		if strings.TrimSpace(taskID) != "" {
			options.ActorType = "agent"
			options.AgentKey = taskSubAgentKey
			if strings.TrimSpace(options.Presentation) == "" {
				options.Presentation = "task"
			}
		} else {
			options.ActorType = "team"
			options.Presentation = "reply"
			options.HideTeamCoordinatorInternals = true
		}
	}
	if err := r.replayStepMessages(rd, line, taskID, stage, options); err != nil {
		return err
	}
	r.accumulateStepUsage(rd, stepUsage)
	if events := finishReplayedSubTaskIfTerminal(rd, runID, taskID, taskStatus, ts, r.nextSeq); len(events) > 0 {
		if r.orchestratedTeam {
			events = decorateReplayedTeamTaskEvents(events, firstNonEmptyReplayString(teamID, r.summary.TeamID), taskSubAgentKey, presentation)
		}
		rd.events = append(rd.events, events...)
	}
	return nil
}

// replayStepMessages keeps waiting prompts beside their tool calls and sources
// beside their results; unmatched items are appended at the end of the step.
func (r *historyReplay) replayStepMessages(rd *chatRunData, line map[string]any, taskID, stage string, options replayMessageOptions) error {
	chatID, _ := line["chatId"].(string)
	runID, _ := line["runId"].(string)
	lineLiveSeq := int64FromAny(line["liveSeq"])
	msgs, _ := line["messages"].([]any)
	awaitingReplay, err := newStepAwaitingReplay(line["awaiting"], chatID, runID, r.chatDir, lineLiveSeq)
	if err != nil {
		return err
	}
	if state := planningStateFromAwaitingPlanning(line["awaiting"], r.chatDir); state != nil {
		r.planning = state
	}
	sourceReplay := newStepSourceReplay(line["sources"], runID, taskID, lineLiveSeq, r.nextSeq)
	for _, rawMsg := range msgs {
		msgMap, _ := rawMsg.(map[string]any)
		if msgMap == nil {
			continue
		}
		messageEvents, err := storedMessageToEventsWithOptions(msgMap, runID, taskID, stage, lineLiveSeq, r.nextSeq, options)
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
