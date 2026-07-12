package chat

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/plantasks"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
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

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return Detail{}, err
	}
	runStartedAt, runCompletedAt, err := s.replayRunLifecycleTimesLocked(chatID)
	if err != nil {
		return Detail{}, err
	}

	rawMessages := rawMessagesFromJSONLLines(lines)

	return parseChatNewFormat(*sum, lines, rawMessages, s.ChatDir(chatID), runStartedAt, runCompletedAt)
}

func (s *FileStore) replayRunLifecycleTimesLocked(chatID string) (map[string]int64, map[string]int64, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_, STARTED_AT_, COMPLETED_AT_, FINISH_REASON_ FROM RUNS WHERE CHAT_ID_=?`, chatID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	startedAt := map[string]int64{}
	completedAt := map[string]int64{}
	for rows.Next() {
		var runID string
		var started, completed int64
		var finishReason string
		if err := rows.Scan(&runID, &started, &completed, &finishReason); err != nil {
			return nil, nil, err
		}
		runID = strings.TrimSpace(runID)
		if runID == "" {
			continue
		}
		if err := timecontract.ValidateEpochMillis(started, "startedAt", "chat.replay.runs["+runID+"].startedAt"); err != nil {
			return nil, nil, err
		}
		startedAt[runID] = started
		if completed == 0 {
			if strings.TrimSpace(finishReason) != "" {
				return nil, nil, &timecontract.Violation{Field: "completedAt", Location: "chat.replay.runs[" + runID + "].completedAt", Reason: "is required"}
			}
			continue
		}
		if err := timecontract.ValidateEpochMillis(completed, "completedAt", "chat.replay.runs["+runID+"].completedAt"); err != nil {
			return nil, nil, err
		}
		completedAt[runID] = completed
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return startedAt, completedAt, nil
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
	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
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
			if lineIsSystemInitQuery(line) {
				continue
			}
			data, _ := json.Marshal(line)
			var query QueryLine
			if err := json.Unmarshal(data, &query); err == nil {
				if strings.TrimSpace(query.TaskID) == "" && trace.Query == nil {
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

func parseChatNewFormat(summary Summary, lines []map[string]any, rawMessages []map[string]any, chatDir string, runStartedAt map[string]int64, runCompletedAt map[string]int64) (Detail, error) {
	if containsLegacyPlanningAwaiting(lines) {
		return Detail{}, ErrLegacyPlanningProtocol
	}
	var planning *PlanningState
	var artifact *ArtifactState

	runs := map[string]*chatRunData{}
	var runOrder []string
	var chatStartEvent *stream.EventData
	orchestratedTeam := strings.EqualFold(strings.TrimSpace(summary.OwnerType), "team") ||
		(strings.TrimSpace(summary.TeamID) != "" && strings.TrimSpace(summary.AgentKey) == "")

	seq := int64(0)
	nextSeq := func() int64 { seq++; return seq }

	var chatTotalPromptTokens, chatTotalCompletionTokens, chatTotalTotalTokens int
	var chatTotalCachedTokens, chatTotalReasoningTokens, chatTotalPromptCacheHitTokens, chatTotalPromptCacheMissTokens int
	var chatTotalLlmChatCompletionCount int
	var chatTotalToolCallCount int
	var chatTotalFirstTokenLatencyMs int64
	var chatTotalFirstTokenLatencyCount int
	var chatTotalGenerationDurationMs int64
	var chatTotalEstimatedCostCurrency string
	var chatTotalEstimatedCostInputHit, chatTotalEstimatedCostInputMiss, chatTotalEstimatedCostOutput, chatTotalEstimatedCostTotal float64
	var latestContextWindow map[string]any
	taskQueries := map[string]replayedSubTaskQuery{}
	for _, line := range lines {
		if lineType, _ := line["_type"].(string); lineType != "query" {
			continue
		}
		if lineIsSystemInitQuery(line) {
			continue
		}
		runID, _ := line["runId"].(string)
		taskID, _ := line["taskId"].(string)
		if strings.TrimSpace(taskID) == "" {
			continue
		}
		query, _ := line["query"].(map[string]any)
		taskQueries[replayedTaskQueryKey(runID, taskID)] = replayedSubTaskQuery{
			TaskID:       taskID,
			TaskName:     stringFromAny(line["taskName"]),
			TaskDesc:     stringFromAny(query["message"]),
			SubAgentKey:  stringFromAny(line["subAgentKey"]),
			MainToolID:   taskToolIDFromLine(line),
			TeamID:       stringFromAny(line["teamId"]),
			Presentation: stringFromAny(line["presentation"]),
			RootContent:  boolFromAny(line["rootContent"]),
		}
	}

	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		chatID, _ := line["chatId"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case "query":
			if lineIsSystemInitQuery(line) {
				continue
			}
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
			if rootContent := boolFromAny(line["rootContent"]); strings.TrimSpace(taskID) != "" && rootContent {
				// Direct delegation is presented as the root reply. Its internal child
				// query must not reappear as a task-scoped request during replay.
				continue
			}
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
					if orchestratedTeam {
						events = decorateReplayedTeamTaskEvents(events, firstNonEmptyReplayString(stringFromAny(line["teamId"]), summary.TeamID), taskSubAgentKey, stringFromAny(line["presentation"]))
					}
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
			teamID := stringFromAny(line["teamId"])
			presentation := stringFromAny(line["presentation"])
			rootContent := false
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
				teamID = firstNonEmptyReplayString(teamID, meta.TeamID)
				presentation = firstNonEmptyReplayString(presentation, meta.Presentation)
				rootContent = meta.RootContent
			}
			ts := int64FromAny(line["updatedAt"])
			if !rootContent {
				if events := beginReplayedSubTask(rd, runID, taskID, taskName, taskDescription, taskSubAgentKey, taskMainToolID, ts, nextSeq); len(events) > 0 {
					if orchestratedTeam {
						events = decorateReplayedTeamTaskEvents(events, firstNonEmptyReplayString(teamID, summary.TeamID), taskSubAgentKey, presentation)
					}
					rd.events = append(rd.events, events...)
				}
			}
			msgs, _ := line["messages"].([]any)
			awaitingReplay, err := newStepAwaitingReplay(line["awaiting"], chatID, runID, chatDir, lineLiveSeq)
			if err != nil {
				return Detail{}, err
			}
			if state := planningStateFromAwaitingPlanning(line["awaiting"], chatDir); state != nil {
				planning = state
			}
			stepUsage, _ := line["usage"].(map[string]any)
			stepContextWindow, _ := line["contextWindow"].(map[string]any)
			stepContextWindow = contextWindowWithStepModelMetadata(stepContextWindow, line)
			if cw := synthesizedUsageSnapshotContextWindow(stepContextWindow); len(cw) > 0 {
				latestContextWindow = cw
			}
			sourceReplay := newStepSourceReplay(line["sources"], runID, taskID, lineLiveSeq, nextSeq)
			for _, rawMsg := range msgs {
				msgMap, _ := rawMsg.(map[string]any)
				if msgMap == nil {
					continue
				}
				options := replayMessageOptions{}
				if orchestratedTeam {
					options.TeamID = firstNonEmptyReplayString(teamID, summary.TeamID)
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
				messageEvents, err := storedMessageToEventsWithOptions(msgMap, runID, taskID, stage, lineLiveSeq, nextSeq, options)
				if err != nil {
					return Detail{}, err
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
					chatTotalFirstTokenLatencyMs += firstTokenLatencyMs
					chatTotalFirstTokenLatencyCount++
				}
				if generationDurationMs > 0 {
					rd.totalGenerationDurationMs += generationDurationMs
					chatTotalGenerationDurationMs += generationDurationMs
				}
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
				rd.chatTotalFirstTokenLatencyMs = chatTotalFirstTokenLatencyMs
				rd.chatTotalFirstTokenLatencyCount = chatTotalFirstTokenLatencyCount
				rd.chatTotalGenerationDurationMs = chatTotalGenerationDurationMs
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
				if orchestratedTeam {
					events = decorateReplayedTeamTaskEvents(events, firstNonEmptyReplayString(teamID, summary.TeamID), taskSubAgentKey, presentation)
				}
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
			if ratio := float64FromJSONValue(line["compressionRatio"]); ratio > 0 {
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
				event, err := stream.ParseEventDataMap(submit, "chat.jsonl.submit")
				if err != nil {
					return Detail{}, err
				}
				rd.events = append(rd.events, event)
			}
			if len(answer) > 0 {
				answer = cloneStringAnyMap(answer)
				clearReplayCursorFields(answer)
				if _, ok := answer["runId"]; !ok && runID != "" {
					answer["runId"] = runID
				}
				addReplayLiveSeq(answer, lineLiveSeq)
				event, err := stream.ParseEventDataMap(answer, "chat.jsonl.answer")
				if err != nil {
					return Detail{}, err
				}
				rd.events = append(rd.events, event)
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
			parsed, err := stream.ParseEventDataMap(event, "chat.jsonl.event")
			if err != nil {
				return Detail{}, err
			}
			rd := ensureRun(runs, &runOrder, runID)
			rd.events = append(rd.events, parsed)
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
		if runID != "" {
			var err error
			runStartTimestamp, err = requiredReplayRunStartedAt(runStartedAt, runID)
			if err != nil {
				return Detail{}, err
			}
		}
		for _, ev := range rd.events {
			if ev.Type == "run.start" {
				if ev.Timestamp != runStartTimestamp {
					return Detail{}, &timecontract.Violation{Field: "timestamp", Location: "chat.replay.runs[" + runID + "].run.start.timestamp", Reason: "does not match registered run start"}
				}
				hasRunStart = true
			}
			if ev.Type == "run.complete" {
				completedAt, ok := runCompletedAt[runID]
				if !ok || ev.Timestamp != completedAt {
					return Detail{}, &timecontract.Violation{Field: "timestamp", Location: "chat.replay.runs[" + runID + "].run.complete.timestamp", Reason: "does not match completed run lifecycle"}
				}
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
			runCompleteTimestamp, completed := runCompletedAt[runID]
			if !completed {
				continue
			}
			if err := timecontract.ValidateEpochMillis(runCompleteTimestamp, "completedAt", "chat.replay.runs["+runID+"].completedAt"); err != nil {
				return Detail{}, err
			}
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

	var plan *PlanState
	if snapshot, err := plantasks.LoadLatest(chatDir); err != nil {
		return Detail{}, err
	} else if snapshot != nil {
		plan = planStateFromTaskSnapshot(snapshot)
	}

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
				PromptTokens:             chatTotalPromptTokens,
				CompletionTokens:         chatTotalCompletionTokens,
				TotalTokens:              chatTotalTotalTokens,
				CachedTokens:             chatTotalCachedTokens,
				ReasoningTokens:          chatTotalReasoningTokens,
				PromptCacheHitTokens:     chatTotalPromptCacheHitTokens,
				PromptCacheMissTokens:    chatTotalPromptCacheMissTokens,
				LlmChatCompletionCount:   chatTotalLlmChatCompletionCount,
				ToolCallCount:            chatTotalToolCallCount,
				EstimatedCostCurrency:    chatTotalEstimatedCostCurrency,
				EstimatedCostInputHit:    chatTotalEstimatedCostInputHit,
				EstimatedCostInputMiss:   chatTotalEstimatedCostInputMiss,
				EstimatedCostOutput:      chatTotalEstimatedCostOutput,
				EstimatedCostTotal:       chatTotalEstimatedCostTotal,
				FirstTokenLatencyTotalMs: chatTotalFirstTokenLatencyMs,
				FirstTokenLatencyCount:   chatTotalFirstTokenLatencyCount,
				GenerationDurationMs:     chatTotalGenerationDurationMs,
			},
		},
		Plan:     plan,
		Planning: planning,
		Artifact: artifact,
	}, nil
}

func firstNonEmptyReplayString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func replayRunLifecycleTimesByRuns(runs []RunSummary, location string) (map[string]int64, map[string]int64, error) {
	startedAt := make(map[string]int64, len(runs))
	completedAt := make(map[string]int64, len(runs))
	for _, run := range runs {
		runID := strings.TrimSpace(run.RunID)
		if runID == "" {
			continue
		}
		if err := timecontract.ValidateEpochMillis(run.StartedAt, "startedAt", location+"["+runID+"].startedAt"); err != nil {
			return nil, nil, err
		}
		startedAt[runID] = run.StartedAt
		if err := timecontract.ValidateEpochMillis(run.CompletedAt, "completedAt", location+"["+runID+"].completedAt"); err != nil {
			return nil, nil, err
		}
		completedAt[runID] = run.CompletedAt
	}
	return startedAt, completedAt, nil
}

func requiredReplayRunStartedAt(startedAtByRunID map[string]int64, runID string) (int64, error) {
	runID = strings.TrimSpace(runID)
	startedAt, ok := startedAtByRunID[runID]
	if !ok {
		return 0, &timecontract.Violation{Field: "startedAt", Location: "chat.replay.runs[" + runID + "].startedAt", Reason: "is required"}
	}
	if err := timecontract.ValidateEpochMillis(startedAt, "startedAt", "chat.replay.runs["+runID+"].startedAt"); err != nil {
		return 0, err
	}
	return startedAt, nil
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
		PromptTokens:             rd.totalPromptTokens,
		CompletionTokens:         rd.totalCompletionTokens,
		TotalTokens:              rd.totalTotalTokens,
		CachedTokens:             rd.totalCachedTokens,
		ReasoningTokens:          rd.totalReasoningTokens,
		PromptCacheHitTokens:     rd.totalPromptCacheHitTokens,
		PromptCacheMissTokens:    rd.totalPromptCacheMissTokens,
		LlmChatCompletionCount:   rd.totalLlmChatCompletionCount,
		ToolCallCount:            rd.totalToolCallCount,
		EstimatedCostCurrency:    rd.estimatedCostCurrency,
		EstimatedCostInputHit:    rd.estimatedCostInputHit,
		EstimatedCostInputMiss:   rd.estimatedCostInputMiss,
		EstimatedCostOutput:      rd.estimatedCostOutput,
		EstimatedCostTotal:       rd.estimatedCostTotal,
		FirstTokenLatencyTotalMs: rd.totalFirstTokenLatencyMs,
		FirstTokenLatencyCount:   rd.totalFirstTokenLatencyCount,
		GenerationDurationMs:     rd.totalGenerationDurationMs,
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

func contextWindowWithStepModelMetadata(contextWindow map[string]any, line map[string]any) map[string]any {
	if len(contextWindow) == 0 {
		return contextWindow
	}
	modelKey := strings.TrimSpace(stringFromAny(contextWindow["modelKey"]))
	reasoningEffort := strings.TrimSpace(stringFromAny(contextWindow["reasoningEffort"]))
	if modelKey == "" {
		modelKey = strings.TrimSpace(stringFromAny(line["modelKey"]))
	}
	if reasoningEffort == "" {
		reasoningEffort = strings.TrimSpace(stringFromAny(line["reasoningEffort"]))
	}
	if modelKey == "" && reasoningEffort == "" {
		return contextWindow
	}
	out := cloneStringAnyMap(contextWindow)
	if modelKey != "" {
		out["modelKey"] = modelKey
	}
	if reasoningEffort != "" {
		out["reasoningEffort"] = reasoningEffort
	}
	return out
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
	inputHit = float64FromJSONValue(estimatedCost["inputCacheHit"])
	inputMiss = float64FromJSONValue(estimatedCost["inputCacheMiss"])
	output = float64FromJSONValue(estimatedCost["output"])
	total = float64FromJSONValue(estimatedCost["total"])
	return
}

func float64FromJSONValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		return 0
	}
}

func extractStepTiming(usage map[string]any) (firstTokenLatencyMs int64, generationDurationMs int64) {
	if usage == nil {
		return 0, 0
	}
	timing, _ := usage["timing"].(map[string]any)
	if len(timing) == 0 {
		return 0, 0
	}
	firstTokenLatencyMs = int64(toIntFromKeys(timing, "firstTokenLatencyMs"))
	if firstTokenLatencyMs <= 0 {
		total := toIntFromKeys(timing, "firstTokenLatencyTotalMs")
		count := toIntFromKeys(timing, "firstTokenLatencyCount")
		if total > 0 && count > 0 {
			firstTokenLatencyMs = int64(total / count)
		}
	}
	generationDurationMs = int64(toIntFromKeys(timing, "generationDurationMs"))
	return firstTokenLatencyMs, generationDurationMs
}

func taskToolIDFromLine(line map[string]any) string {
	return stringFromAny(line["taskToolId"])
}
