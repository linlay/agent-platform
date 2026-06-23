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
		case StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute, StepLineTypeStep:
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

		case StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute, StepLineTypeStep:
			lineLiveSeq := int64FromAny(line["liveSeq"])
			rd := ensureRun(runs, &runOrder, runID)

			if rawPlan, ok := line["plan"].(map[string]any); ok {
				plan = parsePlanFromStep(rawPlan)
			}
			if rawArt, ok := line["artifacts"].(map[string]any); ok {
				artifact = parseArtifactFromStep(rawArt)
			}

			stage, _ := line["stage"].(string)
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
			awaitingReplay := newStepAwaitingReplay(line["awaiting"], chatID, runID, chatDir, lineLiveSeq, int64FromAny(line["updatedAt"]))
			if state := planningStateFromAwaitingPlan(line["awaiting"], chatDir); state != nil {
				planning = state
			}
			stepUsage, _ := line["usage"].(map[string]any)
			stepContextWindow, _ := line["contextWindow"].(map[string]any)
			if cw := synthesizedUsageSnapshotContextWindow(stepContextWindow); len(cw) > 0 {
				latestContextWindow = cw
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
				rd.totalPromptTokens += toIntFromKeys(stepUsage, "promptTokens")
				rd.totalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens")
				rd.totalTotalTokens += toIntFromKeys(stepUsage, "totalTokens")
				rd.totalCachedTokens += stepCacheHitTokens
				rd.totalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "reasoningTokens")
				rd.totalPromptCacheHitTokens += stepCacheHitTokens
				rd.totalPromptCacheMissTokens += stepCacheMissTokens
				rd.totalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount")
				rd.totalToolCallCount += toIntFromKeys(stepUsage, "toolCallCount")
				chatTotalPromptTokens += toIntFromKeys(stepUsage, "promptTokens")
				chatTotalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens")
				chatTotalTotalTokens += toIntFromKeys(stepUsage, "totalTokens")
				chatTotalCachedTokens += stepCacheHitTokens
				chatTotalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "reasoningTokens")
				chatTotalPromptCacheHitTokens += stepCacheHitTokens
				chatTotalPromptCacheMissTokens += stepCacheMissTokens
				chatTotalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount")
				chatTotalToolCallCount += toIntFromKeys(stepUsage, "toolCallCount")
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
			if events := finishReplayedSubTaskIfTerminal(rd, runID, taskID, taskStatus, ts, nextSeq); len(events) > 0 {
				rd.events = append(rd.events, events...)
			}
		case CompactCheckpointLineType:
			if lineIsCompacted(line) {
				continue
			}
			eventRunID := runID
			if strings.TrimSpace(eventRunID) == "" {
				eventRunID = summary.LastRunID
			}
			payload := map[string]any{
				"type":      "context.compact.complete",
				"chatId":    chatID,
				"compactId": stringFromAny(line["compactId"]),
				"trigger":   stringFromAny(line["trigger"]),
			}
			if strings.TrimSpace(eventRunID) != "" {
				payload["runId"] = eventRunID
			}
			if summarySource := strings.TrimSpace(stringFromAny(line["summarySource"])); summarySource != "" {
				payload["summarySource"] = summarySource
			}
			if tokens := int64FromAny(line["preCompactEstimatedTokens"]); tokens > 0 {
				payload["preCompactEstimatedTokens"] = tokens
			}
			if tokens := int64FromAny(line["postCompactEstimatedTokens"]); tokens > 0 {
				payload["postCompactEstimatedTokens"] = tokens
			}
			if ratio, ok := line["compressionRatio"].(float64); ok && ratio > 0 {
				payload["compressionRatio"] = ratio
			}
			rd := ensureRun(runs, &runOrder, eventRunID)
			rd.events = append(rd.events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "context.compact.complete",
				Timestamp: int64FromAny(line["updatedAt"]),
				Payload:   payload,
			})
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
		case "event", "steer":
			lineLiveSeq := int64FromAny(line["liveSeq"])
			event, _ := line["event"].(map[string]any)
			if len(event) == 0 {
				continue
			}
			event = cloneStringAnyMap(event)
			clearReplayCursorFields(event)
			if strings.TrimSpace(stringFromAny(event["type"])) == "planning.snapshot" {
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
			runStartTimestamp = replayRunStartTimestamp(rd.events)
			runCompleteTimestamp = rd.events[len(rd.events)-1].Timestamp
		}
		for _, ev := range rd.events {
			if ev.Type == "run.start" {
				hasRunStart = true
				break
			}
		}
		if !hasRunStart && runID != "" {
			runStart := stream.EventData{
				Seq:       nextSeq(),
				Type:      "run.start",
				Timestamp: runStartTimestamp,
				Payload:   map[string]any{"runId": runID, "chatId": summary.ChatID, "agentKey": summary.AgentKey},
			}
			rd.events = insertReplayRunStart(rd.events, runStart)
		}
		allEvents = append(allEvents, rd.events...)
		// Synthesize run.complete for the frontend (not persisted in JSONL).
		if runID != "" && !(isPendingAwaitingRun(summary, runID) && runHasAwaitingAsk(rd.events)) {
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

func replayRunStartTimestamp(events []stream.EventData) int64 {
	if len(events) == 0 {
		return 0
	}
	for _, event := range events {
		if event.Type == "request.query" && strings.TrimSpace(event.String("taskId")) == "" {
			return event.Timestamp
		}
	}
	return events[0].Timestamp
}

func insertReplayRunStart(events []stream.EventData, runStart stream.EventData) []stream.EventData {
	for index, event := range events {
		if event.Type != "request.query" || strings.TrimSpace(event.String("taskId")) != "" {
			continue
		}
		out := make([]stream.EventData, 0, len(events)+1)
		out = append(out, events[:index+1]...)
		out = append(out, runStart)
		out = append(out, events[index+1:]...)
		return out
	}
	out := make([]stream.EventData, 0, len(events)+1)
	out = append(out, runStart)
	out = append(out, events...)
	return out
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

func runHasAwaitingAsk(events []stream.EventData) bool {
	for _, event := range events {
		if event.Type == "awaiting.ask" {
			return true
		}
	}
	return false
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
