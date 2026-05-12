package chat

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/stream"
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

	// Read {chatId}.jsonl (flat file, Java format). Fallback to {chatId}/events.jsonl (old Go format).
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return Detail{}, err
	}
	if len(lines) == 0 {
		lines, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "events.jsonl"))
		if err != nil {
			return Detail{}, err
		}
	}

	// Load raw messages for includeRawMessages support
	rawMessages := s.loadRawMessagesFromJSONL(chatID)
	if len(rawMessages) == 0 {
		rawMessages, _ = readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
	}

	// Detect format: new format has _type field, old format has type field.
	if isNewFormat(lines) {
		return parseChatNewFormat(*sum, lines, rawMessages)
	}
	return parseChatLegacyFormat(*sum, lines, rawMessages)
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
				trace.Query = &query
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
	var artifact *ArtifactState

	runs := map[string]*chatRunData{}
	var runOrder []string
	var chatStartEvent *stream.EventData

	seq := int64(0)
	nextSeq := func() int64 { seq++; return seq }

	var chatTotalPromptTokens, chatTotalCompletionTokens, chatTotalTotalTokens int

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
			if _, ok := payload["chatId"]; !ok {
				payload["chatId"] = chatID
			}

			rd := ensureRun(runs, &runOrder, runID)
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
			taskGroupID, _ := line["taskGroupId"].(string)
			taskName, _ := line["taskName"].(string)
			taskDescription, _ := line["taskDescription"].(string)
			taskStatus, _ := line["taskStatus"].(string)
			taskSubAgentKey, _ := line["taskSubAgentKey"].(string)
			taskMainToolID, _ := line["taskMainToolId"].(string)
			if events := reconcileReplayedSubTask(rd, runID, taskID, taskGroupID, taskName, taskDescription, taskStatus, taskSubAgentKey, taskMainToolID, int64FromAny(line["updatedAt"]), nextSeq); len(events) > 0 {
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
			ts := int64FromAny(line["updatedAt"])
			if replayDebugEvents {
				runCumulativePre := map[string]int{
					"promptTokens":     rd.totalPromptTokens,
					"completionTokens": rd.totalCompletionTokens,
					"totalTokens":      rd.totalTotalTokens,
				}
				chatCumulativePre := map[string]int{
					"promptTokens":     chatTotalPromptTokens,
					"completionTokens": chatTotalCompletionTokens,
					"totalTokens":      chatTotalTotalTokens,
				}
				if ev := synthesizePreCallEvent(runID, chatID, runCumulativePre, chatCumulativePre, stepContextWindow, stepPreCallData, ts, nextSeq); ev != nil {
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
			if stepUsage != nil {
				rd.totalPromptTokens += toIntFromKeys(stepUsage, "promptTokens", "prompt_tokens")
				rd.totalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens", "completion_tokens")
				rd.totalTotalTokens += toIntFromKeys(stepUsage, "totalTokens", "total_tokens")
				chatTotalPromptTokens += toIntFromKeys(stepUsage, "promptTokens", "prompt_tokens")
				chatTotalCompletionTokens += toIntFromKeys(stepUsage, "completionTokens", "completion_tokens")
				chatTotalTotalTokens += toIntFromKeys(stepUsage, "totalTokens", "total_tokens")
				rd.chatTotalPromptTokens = chatTotalPromptTokens
				rd.chatTotalCompletionTokens = chatTotalCompletionTokens
				rd.chatTotalTotalTokens = chatTotalTotalTokens
			}
			if replayDebugEvents && (stepUsage != nil || len(stepContextWindow) > 0) {
				runCumulativePost := map[string]int{
					"promptTokens":     rd.totalPromptTokens,
					"completionTokens": rd.totalCompletionTokens,
					"totalTokens":      rd.totalTotalTokens,
				}
				chatCumulativePost := map[string]int{
					"promptTokens":     chatTotalPromptTokens,
					"completionTokens": chatTotalCompletionTokens,
					"totalTokens":      chatTotalTotalTokens,
				}
				if ev := synthesizePostCallEvent(runID, chatID, stepUsage, runCumulativePost, chatCumulativePost, stepContextWindow, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
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
			if rd.totalTotalTokens > 0 {
				payload["usage"] = map[string]any{
					"chat": map[string]any{
						"promptTokens":     rd.chatTotalPromptTokens,
						"completionTokens": rd.chatTotalCompletionTokens,
						"totalTokens":      rd.chatTotalTotalTokens,
					},
					"run": map[string]any{
						"promptTokens":     rd.totalPromptTokens,
						"completionTokens": rd.totalCompletionTokens,
						"totalTokens":      rd.totalTotalTokens,
					},
				}
			}
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
		Artifact:    artifact,
	}, nil
}
