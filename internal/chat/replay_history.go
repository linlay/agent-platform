package chat

import (
	"strings"

	"agent-platform/internal/plantasks"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

// replayChatHistory is shared by active and archived chats. It projects stored
// records into Detail; export applies its own document filters to Detail.Events.
func replayChatHistory(summary Summary, lines []map[string]any, rawMessages []map[string]any, chatDir string, runStartedAt map[string]int64, runCompletedAt map[string]int64, runFinishReasons map[string]string) (Detail, error) {
	r := &historyReplay{
		summary: summary, chatDir: chatDir,
		runs: map[string]*chatRunData{}, taskQueries: collectReplayedTaskQueries(lines),
		orchestratedTeam: isTeamOwner(summary.AgentKey, summary.TeamID),
	}
	for _, line := range lines {
		if err := r.replayLine(line); err != nil {
			return Detail{}, err
		}
	}
	allEvents, err := r.finishEvents(runStartedAt, runCompletedAt, runFinishReasons)
	if err != nil {
		return Detail{}, err
	}
	lastRunID, lastRunUsage := latestReplayRunUsage(r.runs, r.runOrder)

	var plan *PlanState
	if snapshot, err := plantasks.LoadLatest(r.chatDir); err != nil {
		return Detail{}, err
	} else if snapshot != nil {
		plan = planStateFromTaskSnapshot(snapshot)
	}

	return Detail{
		ChatID:        r.summary.ChatID,
		ChatName:      r.summary.ChatName,
		RawMessages:   rawMessages,
		Events:        allEvents,
		ContextWindow: r.latestContextWindow,
		ReplayUsage: ReplayUsage{
			LastRunID: lastRunID,
			LastRun:   lastRunUsage,
			Chat:      r.usage,
		},
		Plan:     plan,
		Planning: r.planning,
		Artifact: nil,
	}, nil
}

// historyReplay owns only state that must survive between persisted records.
// Step seq groups stored model/tool turns; nextSeq is temporary replay ordering.
// finishEvents assigns contiguous history seq, while payload liveSeq stays intact.
type historyReplay struct {
	summary             Summary
	chatDir             string
	runs                map[string]*chatRunData
	runOrder            []string
	taskQueries         map[string]replayedSubTaskQuery
	orchestratedTeam    bool
	seq                 int64
	planning            *PlanningState
	latestContextWindow map[string]any
	usage               UsageData
}

func (r *historyReplay) nextSeq() int64 { r.seq++; return r.seq }

func (r *historyReplay) replayLine(line map[string]any) error {
	switch stringFromAny(line["_type"]) {
	case "query":
		return r.replayQuery(line)
	case StepLineTypeReact, StepLineTypeReactTool:
		return r.replayStep(line)
	case CompactCheckpointLineType:
		return r.replayHistoryCompact(line)
	case RunCompactCheckpointLineType:
		return r.replayRunCompact(line)
	case ToolCompactLineType:
		return r.replayToolCompact(line)
	case "submit":
		return r.replaySubmit(line)
	case "event":
		return r.replayEvent(line)
	case "steer":
		return r.replaySteer(line)
	}
	return nil
}

func collectReplayedTaskQueries(lines []map[string]any) map[string]replayedSubTaskQuery {
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

	return taskQueries
}

func (r *historyReplay) replayQuery(line map[string]any) error {
	chatID, _ := line["chatId"].(string)
	runID, _ := line["runId"].(string)
	if lineIsSystemInitQuery(line) {
		return nil
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
		return nil
	}
	if strings.TrimSpace(taskID) != "" {
		payload["taskId"] = taskID
	}
	if _, ok := payload["chatId"]; !ok {
		payload["chatId"] = chatID
	}
	addReplayLiveSeq(payload, lineLiveSeq)

	rd := ensureRun(r.runs, &r.runOrder, runID)
	if strings.TrimSpace(taskID) != "" {
		ts := int64FromAny(line["updatedAt"])
		taskName := stringFromAny(line["taskName"])
		taskDescription := stringFromAny(query["message"])
		taskSubAgentKey := stringFromAny(line["subAgentKey"])
		taskMainToolID := taskToolIDFromLine(line)
		if events := beginReplayedSubTask(rd, runID, taskID, taskName, taskDescription, taskSubAgentKey, taskMainToolID, ts, r.nextSeq); len(events) > 0 {
			if r.orchestratedTeam {
				events = decorateReplayedTeamTaskEvents(events, firstNonEmptyReplayString(stringFromAny(line["teamId"]), r.summary.TeamID), taskSubAgentKey, stringFromAny(line["presentation"]))
			}
			rd.events = append(rd.events, events...)
		}
	}
	rd.events = append(rd.events, stream.EventData{
		Seq:       r.nextSeq(),
		Type:      "request.query",
		Timestamp: int64FromAny(line["updatedAt"]),
		Payload:   payload,
	})
	return nil
}

func (r *historyReplay) finishEvents(runStartedAt, runCompletedAt map[string]int64, runFinishReasons map[string]string) ([]stream.EventData, error) {
	allEvents := make([]stream.EventData, 0)

	if r.summary.ChatName != "" {
		allEvents = append(allEvents, stream.EventData{
			Seq:       r.nextSeq(),
			Type:      "chat.start",
			Timestamp: r.summary.CreatedAt,
			Payload:   map[string]any{"chatId": r.summary.ChatID, "chatName": r.summary.ChatName},
		})
	}

	for _, runID := range r.runOrder {
		events, err := r.finishRun(r.runs[runID], runStartedAt, runCompletedAt, runFinishReasons)
		if err != nil {
			return nil, err
		}
		allEvents = append(allEvents, events...)
	}

	for i := range allEvents {
		allEvents[i].Seq = int64(i + 1)
	}

	return allEvents, nil
}

// finishRun uses the registered lifecycle clocks, never timestamps inferred from
// messages. An unfinished run or a pending wait must not gain a terminal event.
func (r *historyReplay) finishRun(rd *chatRunData, runStartedAt, runCompletedAt map[string]int64, runFinishReasons map[string]string) ([]stream.EventData, error) {
	runID := rd.runID
	if events := flushReplayedSubTask(rd, r.nextSeq); len(events) > 0 {
		rd.events = append(rd.events, events...)
	}
	hasRunStart := false
	runStartTimestamp := int64(0)
	if runID != "" {
		var err error
		runStartTimestamp, err = requiredReplayRunStartedAt(runStartedAt, runID)
		if err != nil {
			return nil, err
		}
	}
	for _, ev := range rd.events {
		if ev.Type == "run.start" {
			if ev.Timestamp != runStartTimestamp {
				return nil, &timecontract.Violation{Field: "timestamp", Location: "chat.replay.runs[" + runID + "].run.start.timestamp", Reason: "does not match registered run start"}
			}
			hasRunStart = true
		}
		if ev.Type == "run.complete" {
			completedAt, ok := runCompletedAt[runID]
			if !ok || ev.Timestamp != completedAt {
				return nil, &timecontract.Violation{Field: "timestamp", Location: "chat.replay.runs[" + runID + "].run.complete.timestamp", Reason: "does not match completed run lifecycle"}
			}
		}
	}
	if !hasRunStart && runID != "" {
		runStart := stream.EventData{
			Seq:       r.nextSeq(),
			Type:      "run.start",
			Timestamp: runStartTimestamp,
			Payload:   map[string]any{"runId": runID, "chatId": r.summary.ChatID, "agentKey": r.summary.AgentKey},
		}
		rd.events = insertReplayRunStart(rd.events, runStart)
	}
	events := rd.events
	// Synthesize the lifecycle terminal event for legacy runs that predate
	// persisted run.error event lines.
	if runID != "" && !(isPendingAwaitingRun(r.summary, runID) && runHasAwaitingAsk(rd.events)) {
		runCompleteTimestamp, completed := runCompletedAt[runID]
		if !completed {
			return events, nil
		}
		if err := timecontract.ValidateEpochMillis(runCompleteTimestamp, "completedAt", "chat.replay.runs["+runID+"].completedAt"); err != nil {
			return nil, err
		}
		terminalType := replayTerminalEventType(runFinishReasons[runID])
		if !hasReplayTerminalEvent(rd.events, terminalType) {
			events = append(events, synthesizedReplayTerminalEvent(runID, terminalType, runCompleteTimestamp, r.nextSeq()))
		}
	}
	return events, nil
}
