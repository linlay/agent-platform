package chat

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/stream"
)

func (s *FileStore) LoadChat(chatID string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return Detail{}, err
	}
	if sum == nil {
		return Detail{}, ErrChatNotFound
	}

	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return Detail{}, err
	}

	rawMessages := s.loadRawMessagesFromJSONL(chatID)

	return parseChatNewFormat(*sum, lines, rawMessages, s.ChatDir(chatID))
}

func (s *FileStore) LoadRunTrace(chatID string, runID string) (RunTrace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return RunTrace{}, err
	}
	if sum == nil {
		return RunTrace{}, ErrChatNotFound
	}
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return RunTrace{}, err
	}
	trace := RunTrace{
		ChatID:   chatID,
		ChatName: sum.ChatName,
		AgentKey: sum.AgentKey,
		TeamID:   sum.TeamID,
		RunID:    runID,
	}
	for _, line := range lines {
		lineRunID, _ := line["runId"].(string)
		if strings.TrimSpace(lineRunID) != strings.TrimSpace(runID) {
			continue
		}
		lineType, _ := line["_type"].(string)
		switch lineType {
		case "query":
			data, _ := json.Marshal(line)
			var query QueryLine
			if err := json.Unmarshal(data, &query); err == nil {
				if strings.TrimSpace(query.TaskID) == "" {
					trace.Query = &query
				}
			}
		case StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute, StepLineTypeLegacyStep:
			data, _ := json.Marshal(line)
			var step StepLine
			if err := json.Unmarshal(data, &step); err == nil {
				trace.Steps = append(trace.Steps, step)
				for _, message := range step.Messages {
					if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
						text := extractStoredMessageText(message)
						if strings.TrimSpace(text) != "" {
							trace.AssistantText = text
						}
					}
				}
			}
		}
	}
	if trace.Query == nil && len(trace.Steps) == 0 {
		return RunTrace{}, ErrChatNotFound
	}
	if strings.TrimSpace(trace.AssistantText) == "" {
		trace.AssistantText = sum.LastRunContent
	}
	return trace, nil
}

// ---------------------------------------------------------------------------
// New format: _type = "query" / "step" / "event" (matching Java)
// ---------------------------------------------------------------------------

func parseChatNewFormat(summary Summary, lines []map[string]any, rawMessages []map[string]any, chatDir string) (Detail, error) {
	var plan *PlanState
	var planning *PlanningState
	var artifact *ArtifactState

	runs := map[string]*chatRunData{}
	var runOrder []string
	var chatStartEvent *stream.EventData

	seq := int64(0)
	nextSeq := func() int64 { seq++; return seq }

	var chatTotalPromptTokens, chatTotalCompletionTokens, chatTotalTotalTokens int
	var chatTotalCachedTokens, chatTotalReasoningTokens, chatTotalPromptCacheHitTokens, chatTotalPromptCacheMissTokens int
	var chatTotalLlmChatCompletionCount int
	var chatTotalToolCallCount int
	var chatTotalEstimatedCostCurrency string
	var chatTotalEstimatedCostInputHit, chatTotalEstimatedCostInputMiss, chatTotalEstimatedCostOutput, chatTotalEstimatedCostTotal float64
	var latestContextWindow map[string]any
	taskQueries := map[string]replayedSubTaskQuery{}
	legacyConfirmIDs := map[string]bool{}
	legacyPlanningSnapshotIDs := legacyPlanningSnapshotIDsFromLines(lines, chatDir)
	for _, line := range lines {
		if lineType, _ := line["_type"].(string); lineType != "query" {
			continue
		}
		runID, _ := line["runId"].(string)
		taskID, _ := line["taskId"].(string)
		if strings.TrimSpace(taskID) == "" {
			continue
		}
		query, _ := line["query"].(map[string]any)
		taskQueries[replayedTaskQueryKey(runID, taskID)] = replayedSubTaskQuery{
			TaskID:      taskID,
			TaskName:    stringFromAny(line["taskName"]),
			TaskDesc:    stringFromAny(query["message"]),
			SubAgentKey: stringFromAny(line["subAgentKey"]),
			MainToolID:  taskToolIDFromLine(line),
		}
	}

	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		chatID, _ := line["chatId"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case "query":
			lineLiveSeq := int64FromAny(line["liveSeq"])
			query, _ := line["query"].(map[string]any)
			if query == nil {
				query = map[string]any{}
			}
			payload := map[string]any{}
			for k, v := range query {
				if k == "seq" || k == "liveSeq" {
					continue
				}
				payload[k] = v
			}
			taskID, _ := line["taskId"].(string)
			if strings.TrimSpace(taskID) != "" {
				payload["taskId"] = taskID
			}
			if _, ok := payload["chatId"]; !ok {
				payload["chatId"] = chatID
			}
			addReplayLiveSeq(payload, lineLiveSeq)

			rd := ensureRun(runs, &runOrder, runID)
			if strings.TrimSpace(taskID) != "" {
				ts := int64FromAny(line["updatedAt"])
				taskName := stringFromAny(line["taskName"])
				taskDescription := stringFromAny(query["message"])
				taskSubAgentKey := stringFromAny(line["subAgentKey"])
				taskMainToolID := taskToolIDFromLine(line)
				if events := beginReplayedSubTask(rd, runID, taskID, taskName, taskDescription, taskSubAgentKey, taskMainToolID, ts, nextSeq); len(events) > 0 {
					rd.events = append(rd.events, events...)
				}
			}
			rd.events = append(rd.events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "request.query",
				Timestamp: int64FromAny(line["updatedAt"]),
				Payload:   payload,
			})

		case StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute, StepLineTypeLegacyStep:
			lineLiveSeq := int64FromAny(line["liveSeq"])
			rd := ensureRun(runs, &runOrder, runID)

			if rawPlan, ok := line["plan"].(map[string]any); ok {
				plan = parsePlanFromStep(rawPlan)
			}
			if rawArt, ok := line["artifacts"].(map[string]any); ok {
				artifact = parseArtifactFromStep(rawArt)
			}

			// new format uses "stage", legacy uses "_stage"
			stage, _ := line["stage"].(string)
			if stage == "" {
				stage, _ = line["_stage"].(string)
			}
			taskID, _ := line["taskId"].(string)
			taskName, _ := line["taskName"].(string)
			taskDescription, _ := line["taskDescription"].(string)
			taskStatus, _ := line["taskStatus"].(string)
			taskSubAgentKey, _ := line["taskSubAgentKey"].(string)
			taskMainToolID := taskToolIDFromLine(line)
			if meta, ok := taskQueries[replayedTaskQueryKey(runID, taskID)]; ok {
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
			}
			ts := int64FromAny(line["updatedAt"])
			if events := beginReplayedSubTask(rd, runID, taskID, taskName, taskDescription, taskSubAgentKey, taskMainToolID, ts, nextSeq); len(events) > 0 {
				rd.events = append(rd.events, events...)
			}
			msgs, _ := line["messages"].([]any)
			awaitingReplay := newStepAwaitingReplay(line["awaiting"], chatID, runID, chatDir, lineLiveSeq, int64FromAny(line["updatedAt"]), legacyPlanningSnapshotIDs)
			if state := planningStateFromAwaitingPlan(line["awaiting"], chatDir); state != nil {
				planning = state
			}
			stepUsage, _ := line["usage"].(map[string]any)
			stepContextWindow, _ := line["contextWindow"].(map[string]any)
			stepContextWindow = contextWindowWithStepModel(line, stepContextWindow, stepUsage)
			if cw := synthesizedUsageSnapshotContextWindow(stepContextWindow); len(cw) > 0 {
				latestContextWindow = cw
			}
			stepSystem, _ := line["system"].(map[string]any)
			stepDebug, _ := line["debug"].(map[string]any)
			stepPreCallData := debugPreCallData(stepDebug, stepSystem)
			replayDebugEvents := len(stepPreCallData) > 0
			if replayDebugEvents {
				runCumulativePre := map[string]int{
					"promptTokens":           rd.totalPromptTokens,
					"completionTokens":       rd.totalCompletionTokens,
					"totalTokens":            rd.totalTotalTokens,
					"cachedTokens":           rd.totalCachedTokens,
					"reasoningTokens":        rd.totalReasoningTokens,
					"promptCacheHitTokens":   rd.totalPromptCacheHitTokens,
					"promptCacheMissTokens":  rd.totalPromptCacheMissTokens,
					"llmChatCompletionCount": rd.totalLlmChatCompletionCount,
					"toolCallCount":          rd.totalToolCallCount,
				}
				chatCumulativePre := map[string]int{
					"promptTokens":           chatTotalPromptTokens,
					"completionTokens":       chatTotalCompletionTokens,
					"totalTokens":            chatTotalTotalTokens,
					"cachedTokens":           chatTotalCachedTokens,
					"reasoningTokens":        chatTotalReasoningTokens,
					"promptCacheHitTokens":   chatTotalPromptCacheHitTokens,
					"promptCacheMissTokens":  chatTotalPromptCacheMissTokens,
					"llmChatCompletionCount": chatTotalLlmChatCompletionCount,
					"toolCallCount":          chatTotalToolCallCount,
				}
				if ev := synthesizePreCallEvent(runID, chatID, taskID, runCumulativePre, chatCumulativePre, stepContextWindow, stepPreCallData, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
			}
			for _, rawMsg := range msgs {
				msgMap, _ := rawMsg.(map[string]any)
				if msgMap == nil {
					continue
				}
				for _, ev := range storedMessageToEvents(msgMap, runID, taskID, stage, lineLiveSeq, nextSeq) {
					rd.events = append(rd.events, ev)
					if ev.Type == "tool.snapshot" {
						rd.events = append(rd.events, awaitingReplay.consumeForTool(ev.String("toolId"))...)
					}
				}
			}
			rd.events = append(rd.events, awaitingReplay.leftoverEvents()...)
			if hasProviderUsagePayload(stepUsage) {
				stepCacheHitTokens := usageCacheHitTokensFromMap(stepUsage)
				stepCacheMissTokens := usageCacheMissTokensFromMap(stepUsage)
				rd.totalPromptTokens += toIntFromKeys(stepUsage, "promptTokens", "prompt_tokens")
				rd.totalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens", "completion_tokens")
				rd.totalTotalTokens += toIntFromKeys(stepUsage, "totalTokens", "total_tokens")
				rd.totalCachedTokens += stepCacheHitTokens
				rd.totalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "completion_tokens_details", "reasoningTokens", "reasoning_tokens")
				rd.totalPromptCacheHitTokens += stepCacheHitTokens
				rd.totalPromptCacheMissTokens += stepCacheMissTokens
				rd.totalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount", "llm_chat_completion_count")
				rd.totalToolCallCount += toIntFromKeys(stepUsage, "toolCallCount", "tool_call_count")
				chatTotalPromptTokens += toIntFromKeys(stepUsage, "promptTokens", "prompt_tokens")
				chatTotalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens", "completion_tokens")
				chatTotalTotalTokens += toIntFromKeys(stepUsage, "totalTokens", "total_tokens")
				chatTotalCachedTokens += stepCacheHitTokens
				chatTotalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "completion_tokens_details", "reasoningTokens", "reasoning_tokens")
				chatTotalPromptCacheHitTokens += stepCacheHitTokens
				chatTotalPromptCacheMissTokens += stepCacheMissTokens
				chatTotalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount", "llm_chat_completion_count")
				chatTotalToolCallCount += toIntFromKeys(stepUsage, "toolCallCount", "tool_call_count")
				rd.chatTotalPromptTokens = chatTotalPromptTokens
				rd.chatTotalCompletionTokens = chatTotalCompletionTokens
				rd.chatTotalTotalTokens = chatTotalTotalTokens
				rd.chatTotalCachedTokens = chatTotalCachedTokens
				rd.chatTotalReasoningTokens = chatTotalReasoningTokens
				rd.chatTotalPromptCacheHitTokens = chatTotalPromptCacheHitTokens
				rd.chatTotalPromptCacheMissTokens = chatTotalPromptCacheMissTokens
				rd.chatTotalLlmChatCompletionCount = chatTotalLlmChatCompletionCount
				rd.chatTotalToolCallCount = chatTotalToolCallCount
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
				if chatTotalEstimatedCostCurrency == "" {
					chatTotalEstimatedCostCurrency = currency
				}
				chatTotalEstimatedCostInputHit += inputHit
				chatTotalEstimatedCostInputMiss += inputMiss
				chatTotalEstimatedCostOutput += output
				chatTotalEstimatedCostTotal += total
			}
			if replayDebugEvents && (hasProviderUsagePayload(stepUsage) || len(stepContextWindow) > 0) {
				runCumulativePost := map[string]int{
					"promptTokens":           rd.totalPromptTokens,
					"completionTokens":       rd.totalCompletionTokens,
					"totalTokens":            rd.totalTotalTokens,
					"cachedTokens":           rd.totalCachedTokens,
					"reasoningTokens":        rd.totalReasoningTokens,
					"promptCacheHitTokens":   rd.totalPromptCacheHitTokens,
					"promptCacheMissTokens":  rd.totalPromptCacheMissTokens,
					"llmChatCompletionCount": rd.totalLlmChatCompletionCount,
					"toolCallCount":          rd.totalToolCallCount,
				}
				chatCumulativePost := map[string]int{
					"promptTokens":           chatTotalPromptTokens,
					"completionTokens":       chatTotalCompletionTokens,
					"totalTokens":            chatTotalTotalTokens,
					"cachedTokens":           chatTotalCachedTokens,
					"reasoningTokens":        chatTotalReasoningTokens,
					"promptCacheHitTokens":   chatTotalPromptCacheHitTokens,
					"promptCacheMissTokens":  chatTotalPromptCacheMissTokens,
					"llmChatCompletionCount": chatTotalLlmChatCompletionCount,
					"toolCallCount":          chatTotalToolCallCount,
				}
				if ev := synthesizePostCallEvent(runID, chatID, taskID, stepUsage, runCumulativePost, chatCumulativePost, stepContextWindow, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
			}
			if events := finishReplayedSubTaskIfTerminal(rd, runID, taskID, taskStatus, ts, nextSeq); len(events) > 0 {
				rd.events = append(rd.events, events...)
			}
		case "submit":
			lineLiveSeq := int64FromAny(line["liveSeq"])
			rd := ensureRun(runs, &runOrder, runID)
			submit, _ := line["submit"].(map[string]any)
			answer, _ := line["answer"].(map[string]any)
			if len(submit) > 0 {
				submit = cloneStringAnyMap(submit)
				clearReplayCursorFields(submit)
				if _, ok := submit["runId"]; !ok && runID != "" {
					submit["runId"] = runID
				}
				addReplayLiveSeq(submit, lineLiveSeq)
				rd.events = append(rd.events, stream.EventDataFromMap(submit))
			}
			if len(answer) > 0 {
				answer = cloneStringAnyMap(answer)
				clearReplayCursorFields(answer)
				if _, ok := answer["runId"]; !ok && runID != "" {
					answer["runId"] = runID
				}
				addReplayLiveSeq(answer, lineLiveSeq)
				rd.events = append(rd.events, stream.EventDataFromMap(answer))
			}
		case "planning":
			state, event := planningSnapshotFromLine(line, chatDir)
			if state == nil || event == nil {
				continue
			}
			planning = state
			rd := ensureRun(runs, &runOrder, runID)
			event.Seq = nextSeq()
			rd.events = append(rd.events, *event)
		case "event", "steer":
			lineLiveSeq := int64FromAny(line["liveSeq"])
			event, _ := line["event"].(map[string]any)
			if len(event) == 0 {
				continue
			}
			event = cloneStringAnyMap(event)
			clearReplayCursorFields(event)
			if strings.TrimSpace(stringFromAny(event["type"])) == "planning.snapshot" {
				planningLine := cloneStringAnyMap(line)
				planningLine["event"] = event
				state, replayedEvent := planningSnapshotFromLine(planningLine, chatDir)
				if state == nil || replayedEvent == nil {
					continue
				}
				planning = state
				rd := ensureRun(runs, &runOrder, runID)
				replayedEvent.Seq = nextSeq()
				rd.events = append(rd.events, *replayedEvent)
				continue
			}
			if suppressLegacyConfirmReplay(event, legacyConfirmIDs) {
				continue
			}
			if strings.TrimSpace(stringFromAny(event["type"])) == "awaiting.ask" {
				continue
			}
			if _, ok := event["runId"]; !ok && runID != "" {
				event["runId"] = runID
			}
			addReplayLiveSeq(event, lineLiveSeq)
			rd := ensureRun(runs, &runOrder, runID)
			rd.events = append(rd.events, stream.EventDataFromMap(event))
		}
	}

	allEvents := make([]stream.EventData, 0)

	if chatStartEvent == nil && summary.ChatName != "" {
		allEvents = append(allEvents, stream.EventData{
			Seq:       nextSeq(),
			Type:      "chat.start",
			Timestamp: summary.CreatedAt,
			Payload:   map[string]any{"chatId": summary.ChatID, "chatName": summary.ChatName},
		})
	}

	for _, runID := range runOrder {
		rd := runs[runID]
		if events := flushReplayedSubTask(rd, nextSeq); len(events) > 0 {
			rd.events = append(rd.events, events...)
		}
		hasRunStart := false
		runStartTimestamp := int64(0)
		runCompleteTimestamp := int64(0)
		if len(rd.events) > 0 {
			runStartTimestamp = rd.events[0].Timestamp
			runCompleteTimestamp = rd.events[len(rd.events)-1].Timestamp
		}
		for _, ev := range rd.events {
			if ev.Type == "run.start" {
				hasRunStart = true
				break
			}
		}
		if !hasRunStart && runID != "" {
			allEvents = append(allEvents, stream.EventData{
				Seq:       nextSeq(),
				Type:      "run.start",
				Timestamp: runStartTimestamp,
				Payload:   map[string]any{"runId": runID, "chatId": summary.ChatID, "agentKey": summary.AgentKey},
			})
		}
		allEvents = append(allEvents, rd.events...)
		// Synthesize run.complete for the frontend (not persisted in JSONL).
		if runID != "" && !isPendingAwaitingRun(summary, runID) {
			payload := map[string]any{"runId": runID, "finishReason": "stop"}
			allEvents = append(allEvents, stream.EventData{
				Seq:       nextSeq(),
				Type:      "run.complete",
				Timestamp: runCompleteTimestamp,
				Payload:   payload,
			})
		}
	}

	for i := range allEvents {
		allEvents[i].Seq = int64(i + 1)
	}

	lastRunID, lastRunUsage := latestReplayRunUsage(runs, runOrder)

	return Detail{
		ChatID:        summary.ChatID,
		ChatName:      summary.ChatName,
		RawMessages:   rawMessages,
		Events:        allEvents,
		ContextWindow: latestContextWindow,
		ReplayUsage: ReplayUsage{
			LastRunID: lastRunID,
			LastRun:   lastRunUsage,
			Chat: UsageData{
				PromptTokens:           chatTotalPromptTokens,
				CompletionTokens:       chatTotalCompletionTokens,
				TotalTokens:            chatTotalTotalTokens,
				CachedTokens:           chatTotalCachedTokens,
				ReasoningTokens:        chatTotalReasoningTokens,
				PromptCacheHitTokens:   chatTotalPromptCacheHitTokens,
				PromptCacheMissTokens:  chatTotalPromptCacheMissTokens,
				LlmChatCompletionCount: chatTotalLlmChatCompletionCount,
				ToolCallCount:          chatTotalToolCallCount,
				EstimatedCostCurrency:  chatTotalEstimatedCostCurrency,
				EstimatedCostInputHit:  chatTotalEstimatedCostInputHit,
				EstimatedCostInputMiss: chatTotalEstimatedCostInputMiss,
				EstimatedCostOutput:    chatTotalEstimatedCostOutput,
				EstimatedCostTotal:     chatTotalEstimatedCostTotal,
			},
		},
		Plan:     plan,
		Planning: planning,
		Artifact: artifact,
	}, nil
}

func latestReplayRunUsage(runs map[string]*chatRunData, runOrder []string) (string, UsageData) {
	for i := len(runOrder) - 1; i >= 0; i-- {
		runID := strings.TrimSpace(runOrder[i])
		if runID == "" {
			continue
		}
		usage := replayRunUsageData(runs[runID])
		if hasUsageData(usage) {
			return runID, usage
		}
	}
	return "", UsageData{}
}

func replayRunUsageData(rd *chatRunData) UsageData {
	if rd == nil {
		return UsageData{}
	}
	return UsageData{
		PromptTokens:           rd.totalPromptTokens,
		CompletionTokens:       rd.totalCompletionTokens,
		TotalTokens:            rd.totalTotalTokens,
		CachedTokens:           rd.totalCachedTokens,
		ReasoningTokens:        rd.totalReasoningTokens,
		PromptCacheHitTokens:   rd.totalPromptCacheHitTokens,
		PromptCacheMissTokens:  rd.totalPromptCacheMissTokens,
		LlmChatCompletionCount: rd.totalLlmChatCompletionCount,
		ToolCallCount:          rd.totalToolCallCount,
		EstimatedCostCurrency:  rd.estimatedCostCurrency,
		EstimatedCostInputHit:  rd.estimatedCostInputHit,
		EstimatedCostInputMiss: rd.estimatedCostInputMiss,
		EstimatedCostOutput:    rd.estimatedCostOutput,
		EstimatedCostTotal:     rd.estimatedCostTotal,
	}
}

func suppressLegacyConfirmReplay(event map[string]any, legacyConfirmIDs map[string]bool) bool {
	eventType := strings.TrimSpace(stringFromAny(event["type"]))
	switch eventType {
	case "confirm.viewport", "confirm.payload":
		if confirmID := strings.TrimSpace(stringFromAny(event["confirmId"])); confirmID != "" {
			legacyConfirmIDs[confirmID] = true
		}
		return true
	case "request.submit":
		awaitingID := strings.TrimSpace(stringFromAny(event["awaitingId"]))
		if awaitingID == "" || !legacyConfirmIDs[awaitingID] {
			return false
		}
		_, ok := event["params"].([]any)
		return !ok
	default:
		return false
	}
}

func isPendingAwaitingRun(summary Summary, runID string) bool {
	if summary.PendingAwaiting == nil {
		return false
	}
	pendingRunID := strings.TrimSpace(summary.PendingAwaiting.RunID)
	return pendingRunID != "" && pendingRunID == strings.TrimSpace(runID)
}

func extractStepCost(usage map[string]any) (currency string, inputHit, inputMiss, output, total float64) {
	estimatedCost, _ := usage["estimatedCost"].(map[string]any)
	if estimatedCost == nil {
		return
	}
	currency, _ = estimatedCost["currency"].(string)
	currency = strings.TrimSpace(currency)
	if currency == "" {
		return
	}
	inputHit, _ = estimatedCost["inputCacheHit"].(float64)
	inputMiss, _ = estimatedCost["inputCacheMiss"].(float64)
	output, _ = estimatedCost["output"].(float64)
	total, _ = estimatedCost["total"].(float64)
	return
}

func taskToolIDFromLine(line map[string]any) string {
	return stringFromAny(line["taskToolId"])
}
