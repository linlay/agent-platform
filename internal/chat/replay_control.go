package chat

import (
	"strings"

	"agent-platform/internal/stream"
)

func (r *historyReplay) replayHistoryCompact(line map[string]any) error {
	chatID, _ := line["chatId"].(string)
	runID, _ := line["runId"].(string)
	if lineIsCompacted(line) {
		return nil
	}
	eventRunID := runID
	if strings.TrimSpace(eventRunID) == "" {
		eventRunID = r.summary.LastRunID
	}
	payload := map[string]any{
		"type":      "context.compact.complete",
		"chatId":    chatID,
		"compactId": stringFromAny(line["compactId"]),
		"trigger":   stringFromAny(line["trigger"]),
		"level":     "summary",
		"scope":     "history",
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
	if ratio := float64FromJSONValue(line["remainingRatio"]); ratio > 0 {
		payload["remainingRatio"] = ratio
	}
	if ratio := float64FromJSONValue(line["releasedRatio"]); ratio > 0 {
		payload["releasedRatio"] = ratio
	}
	if tokens := int64FromAny(line["tokensFreed"]); tokens > 0 {
		payload["tokensFreed"] = tokens
	}
	if usage, ok := line["compactionUsage"].(map[string]any); ok && len(usage) > 0 {
		payload["compactionUsage"] = cloneStringAnyMap(usage)
	}
	rd := ensureRun(r.runs, &r.runOrder, eventRunID)
	rd.events = append(rd.events, stream.EventData{
		Seq:       r.nextSeq(),
		Type:      "context.compact.complete",
		Timestamp: int64FromAny(line["updatedAt"]),
		Payload:   payload,
	})
	return nil
}

func (r *historyReplay) replayRunCompact(line map[string]any) error {
	chatID, _ := line["chatId"].(string)
	runID, _ := line["runId"].(string)
	if lineIsCompacted(line) {
		return nil
	}
	payload := map[string]any{
		"type":      "context.compact.complete",
		"chatId":    chatID,
		"runId":     runID,
		"compactId": stringFromAny(line["compactId"]),
		"requestId": stringFromAny(line["requestId"]),
		"trigger":   firstNonEmptyReplayString(stringFromAny(line["trigger"]), "auto"),
		"level":     firstNonEmptyReplayString(stringFromAny(line["level"]), "summary"),
		"scope":     "run",
	}
	if source := strings.TrimSpace(stringFromAny(line["summarySource"])); source != "" {
		payload["summarySource"] = source
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
	if ratio := float64FromJSONValue(line["remainingRatio"]); ratio > 0 {
		payload["remainingRatio"] = ratio
	}
	if ratio := float64FromJSONValue(line["releasedRatio"]); ratio > 0 {
		payload["releasedRatio"] = ratio
	}
	if tokens := int64FromAny(line["tokensFreed"]); tokens > 0 {
		payload["tokensFreed"] = tokens
	}
	if count := int64FromAny(line["toolsCleared"]); count > 0 {
		payload["toolsCleared"] = count
	}
	if count := int64FromAny(line["toolsKept"]); count > 0 {
		payload["toolsKept"] = count
	}
	if usage, ok := line["compactionUsage"].(map[string]any); ok && len(usage) > 0 {
		payload["compactionUsage"] = cloneStringAnyMap(usage)
	}
	rd := ensureRun(r.runs, &r.runOrder, runID)
	rd.events = append(rd.events, stream.EventData{
		Seq:       r.nextSeq(),
		Type:      "context.compact.complete",
		Timestamp: int64FromAny(line["updatedAt"]),
		Payload:   payload,
	})
	return nil
}

func (r *historyReplay) replayToolCompact(line map[string]any) error {
	chatID, _ := line["chatId"].(string)
	if lineIsCompacted(line) {
		return nil
	}
	eventRunID := strings.TrimSpace(r.summary.LastRunID)
	payload := map[string]any{
		"type":      "context.compact.complete",
		"chatId":    chatID,
		"compactId": stringFromAny(line["compactId"]),
		"trigger":   firstNonEmptyReplayString(stringFromAny(line["trigger"]), "manual"),
		"level":     "l1_tools",
		"scope":     "history",
	}
	if eventRunID != "" {
		payload["runId"] = eventRunID
	}
	for _, key := range []string{"preCompactEstimatedTokens", "postCompactEstimatedTokens", "tokensFreed", "toolsCleared", "toolsKept"} {
		if value := int64FromAny(line[key]); value > 0 {
			payload[key] = value
		}
	}
	for _, key := range []string{"compressionRatio", "remainingRatio", "releasedRatio"} {
		if value := float64FromJSONValue(line[key]); value > 0 {
			payload[key] = value
		}
	}
	rd := ensureRun(r.runs, &r.runOrder, eventRunID)
	rd.events = append(rd.events, stream.EventData{
		Seq: r.nextSeq(), Type: "context.compact.complete", Timestamp: int64FromAny(line["updatedAt"]), Payload: payload,
	})
	return nil
}

func (r *historyReplay) replaySubmit(line map[string]any) error {
	runID, _ := line["runId"].(string)
	lineLiveSeq := int64FromAny(line["liveSeq"])
	rd := ensureRun(r.runs, &r.runOrder, runID)
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
			return err
		}
		rd.events = append(rd.events, event)
	}
	if len(answer) > 0 {
		answer = replayAwaitingAnswerPayload(answer, int64FromAny(line["updatedAt"]))
		clearReplayCursorFields(answer)
		if _, ok := answer["runId"]; !ok && runID != "" {
			answer["runId"] = runID
		}
		addReplayLiveSeq(answer, lineLiveSeq)
		event, err := stream.ParseEventDataMap(answer, "chat.jsonl.answer")
		if err != nil {
			return err
		}
		rd.events = append(rd.events, event)
	}
	return nil
}

func (r *historyReplay) replayEvent(line map[string]any) error {
	runID, _ := line["runId"].(string)
	lineLiveSeq := int64FromAny(line["liveSeq"])
	event, _ := line["event"].(map[string]any)
	if len(event) == 0 {
		return nil
	}
	event = cloneStringAnyMap(event)
	clearReplayCursorFields(event)
	if strings.TrimSpace(stringFromAny(event["type"])) == "planning.snapshot" {
		return nil
	}
	if strings.TrimSpace(stringFromAny(event["type"])) == "awaiting.ask" {
		return nil
	}
	if _, ok := event["runId"]; !ok && runID != "" {
		event["runId"] = runID
	}
	addReplayLiveSeq(event, lineLiveSeq)
	parsed, err := stream.ParseEventDataMap(event, "chat.jsonl.event")
	if err != nil {
		return err
	}
	rd := ensureRun(r.runs, &r.runOrder, runID)
	rd.events = append(rd.events, parsed)
	return nil
}

func (r *historyReplay) replaySteer(line map[string]any) error {
	runID, _ := line["runId"].(string)
	steer := cloneStringAnyMap(anyMap(line["steer"]))
	addReplayLiveSeq(steer, int64FromAny(line["liveSeq"]))
	rd := ensureRun(r.runs, &r.runOrder, runID)
	rd.events = append(rd.events, stream.EventData{
		Type:      "request.steer",
		Timestamp: int64FromAny(line["updatedAt"]),
		Payload:   steer,
	})
	return nil
}
