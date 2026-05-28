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

	return parseChatNewFormat(*sum, lines, rawMessages)
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
		case "react", "plan-execute", "step":
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

func parseChatNewFormat(summary Summary, lines []map[string]any, rawMessages []map[string]any) (Detail, error) {
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
			query, _ := line["query"].(map[string]any)
			if query == nil {
				query = map[string]any{}
			}
			payload := map[string]any{}
			for k, v := range query {
				payload[k] = v
			}
			taskID, _ := line["taskId"].(string)
			if strings.TrimSpace(taskID) != "" {
				payload["taskId"] = taskID
			}
			if _, ok := payload["chatId"]; !ok {
				payload["chatId"] = chatID
			}

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

		case "react", "plan-execute", "step":
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
			awaitingReplay := newStepAwaitingReplay(line["awaiting"], runID)
			stepUsage, _ := line["usage"].(map[string]any)
			stepContextWindow, _ := line["contextWindow"].(map[string]any)
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
				for _, ev := range storedMessageToEvents(msgMap, runID, taskID, stage, nextSeq) {
					rd.events = append(rd.events, ev)
					if ev.Type == "tool.snapshot" {
						rd.events = append(rd.events, awaitingReplay.consumeForTool(ev.String("toolId"))...)
					}
				}
			}
			rd.events = append(rd.events, awaitingReplay.leftoverEvents()...)
			if hasProviderUsagePayload(stepUsage) {
				rd.totalPromptTokens += toIntFromKeys(stepUsage, "promptTokens", "prompt_tokens")
				rd.totalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens", "completion_tokens")
				rd.totalTotalTokens += toIntFromKeys(stepUsage, "totalTokens", "total_tokens")
				rd.totalCachedTokens += toNestedIntFromKeys(stepUsage, "promptTokensDetails", "prompt_tokens_details", "cachedTokens", "cached_tokens")
				rd.totalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "completion_tokens_details", "reasoningTokens", "reasoning_tokens")
				rd.totalPromptCacheHitTokens += toIntFromKeys(stepUsage, "promptCacheHitTokens", "prompt_cache_hit_tokens")
				rd.totalPromptCacheMissTokens += toIntFromKeys(stepUsage, "promptCacheMissTokens", "prompt_cache_miss_tokens")
				rd.totalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount", "llm_chat_completion_count")
				chatTotalPromptTokens += toIntFromKeys(stepUsage, "promptTokens", "prompt_tokens")
				chatTotalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens", "completion_tokens")
				chatTotalTotalTokens += toIntFromKeys(stepUsage, "totalTokens", "total_tokens")
				chatTotalCachedTokens += toNestedIntFromKeys(stepUsage, "promptTokensDetails", "prompt_tokens_details", "cachedTokens", "cached_tokens")
				chatTotalReasoningTokens += toNestedIntFromKeys(stepUsage, "completionTokensDetails", "completion_tokens_details", "reasoningTokens", "reasoning_tokens")
				chatTotalPromptCacheHitTokens += toIntFromKeys(stepUsage, "promptCacheHitTokens", "prompt_cache_hit_tokens")
				chatTotalPromptCacheMissTokens += toIntFromKeys(stepUsage, "promptCacheMissTokens", "prompt_cache_miss_tokens")
				chatTotalLlmChatCompletionCount += toIntFromKeys(stepUsage, "llmChatCompletionCount", "llm_chat_completion_count")
				rd.chatTotalPromptTokens = chatTotalPromptTokens
				rd.chatTotalCompletionTokens = chatTotalCompletionTokens
				rd.chatTotalTotalTokens = chatTotalTotalTokens
				rd.chatTotalCachedTokens = chatTotalCachedTokens
				rd.chatTotalReasoningTokens = chatTotalReasoningTokens
				rd.chatTotalPromptCacheHitTokens = chatTotalPromptCacheHitTokens
				rd.chatTotalPromptCacheMissTokens = chatTotalPromptCacheMissTokens
				rd.chatTotalLlmChatCompletionCount = chatTotalLlmChatCompletionCount
			}
			if hasProviderUsagePayload(stepUsage) {
				runCumulativePost := map[string]int{
					"promptTokens":           rd.totalPromptTokens,
					"completionTokens":       rd.totalCompletionTokens,
					"totalTokens":            rd.totalTotalTokens,
					"cachedTokens":           rd.totalCachedTokens,
					"reasoningTokens":        rd.totalReasoningTokens,
					"promptCacheHitTokens":   rd.totalPromptCacheHitTokens,
					"promptCacheMissTokens":  rd.totalPromptCacheMissTokens,
					"llmChatCompletionCount": rd.totalLlmChatCompletionCount,
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
				}
				if ev := synthesizeUsageSnapshotEvent(runID, chatID, taskID, stepUsage, runCumulativePost, chatCumulativePost, stepContextWindow, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
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
				}
				if ev := synthesizePostCallEvent(runID, chatID, taskID, stepUsage, runCumulativePost, chatCumulativePost, stepContextWindow, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
			}
			if events := finishReplayedSubTaskIfTerminal(rd, runID, taskID, taskStatus, ts, nextSeq); len(events) > 0 {
				rd.events = append(rd.events, events...)
			}
		case "submit":
			rd := ensureRun(runs, &runOrder, runID)
			submit, _ := line["submit"].(map[string]any)
			answer, _ := line["answer"].(map[string]any)
			if len(submit) > 0 {
				if _, ok := submit["runId"]; !ok && runID != "" {
					submit["runId"] = runID
				}
				rd.events = append(rd.events, stream.EventDataFromMap(submit))
			}
			if len(answer) > 0 {
				if _, ok := answer["runId"]; !ok && runID != "" {
					answer["runId"] = runID
				}
				rd.events = append(rd.events, stream.EventDataFromMap(answer))
			}
		case "compact":
			data, _ := json.Marshal(line)
			var compact CompactLine
			if err := json.Unmarshal(data, &compact); err != nil || strings.TrimSpace(compact.CompactID) == "" {
				continue
			}
			if strings.TrimSpace(runID) == "" {
				runID = strings.TrimSpace(compact.BoundaryRunID)
			}
			if strings.TrimSpace(runID) == "" {
				runID = strings.TrimSpace(summary.LastRunID)
			}
			rd := ensureRun(runs, &runOrder, runID)
			payload := map[string]any{
				"chatId":                     compact.ChatID,
				"runId":                      runID,
				"compactId":                  compact.CompactID,
				"boundaryRunId":              compact.BoundaryRunID,
				"boundarySeq":                compact.BoundarySeq,
				"generation":                 compact.Generation,
				"summarySource":              compact.SummarySource,
				"keptRunCount":               compact.KeptRunCount,
				"compactedRunCount":          compact.CompactedRunCount,
				"toolDigestCount":            len(compact.ToolDigests),
				"digestedRunIds":             append([]string(nil), compact.DigestedRunIDs...),
				"originalMessages":           compact.OriginalMessages,
				"projectedMessages":          compact.ProjectedMessages,
				"preCompactEstimatedTokens":  compact.PreCompactTokens,
				"postCompactEstimatedTokens": compact.PostCompactTokens,
				"compressionRatio":           compact.CompressionRatio,
				"elapsedMs":                  compact.ElapsedMs,
				"trigger":                    compact.Trigger,
			}
			if len(compact.CompactionUsage) > 0 {
				payload["compactionUsage"] = compact.CompactionUsage
			}
			if len(compact.CacheMetrics) > 0 {
				payload["cacheMetrics"] = compact.CacheMetrics
			}
			if strings.TrimSpace(compact.Error) != "" {
				payload["error"] = compact.Error
			}
			rd.events = append(rd.events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "context.compact.complete",
				Timestamp: compact.UpdatedAt,
				Payload:   payload,
			})
		case "planning":
			event, _ := line["event"].(map[string]any)
			if len(event) == 0 {
				continue
			}
			if _, ok := event["runId"]; !ok && runID != "" {
				event["runId"] = runID
			}
			if _, ok := event["chatId"]; !ok && chatID != "" {
				event["chatId"] = chatID
			}
			if nextPlanning := parsePlanningFromEvent(event); nextPlanning != nil {
				planning = nextPlanning
			}
			rd := ensureRun(runs, &runOrder, runID)
			rd.events = append(rd.events, stream.EventDataFromMap(event))
		case "event", "steer":
			event, _ := line["event"].(map[string]any)
			if len(event) == 0 {
				continue
			}
			if _, ok := event["runId"]; !ok && runID != "" {
				event["runId"] = runID
			}
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
		if runID != "" {
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

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		RawMessages: rawMessages,
		Events:      allEvents,
		Plan:        plan,
		Planning:    planning,
		Artifact:    artifact,
	}, nil
}

func taskToolIDFromLine(line map[string]any) string {
	return stringFromAny(line["taskToolId"])
}

func parsePlanningFromEvent(event map[string]any) *PlanningState {
	if len(event) == 0 {
		return nil
	}
	planningID := strings.TrimSpace(stringFromAny(event["planningId"]))
	planningFile := strings.TrimSpace(stringFromAny(event["planningFile"]))
	if planningID == "" && planningFile == "" {
		return nil
	}
	return &PlanningState{
		PlanningID:   planningID,
		PlanningFile: planningFile,
		Title:        strings.TrimSpace(stringFromAny(event["title"])),
		Status:       strings.TrimSpace(stringFromAny(event["status"])),
		Markdown:     strings.TrimSpace(stringFromAny(event["markdown"])),
		UpdatedAt:    int64FromAny(event["updatedAt"]),
	}
}
