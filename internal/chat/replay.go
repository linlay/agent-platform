package chat

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/apperrors"
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
	lines, err = s.logicalHistoryLines(chatID, lines)
	if err != nil {
		return Detail{}, err
	}
	runStartedAt, runCompletedAt, runFinishReasons, err := s.replayRunLifecycleTimesLocked(chatID)
	if err != nil {
		return Detail{}, err
	}

	rawMessages := rawMessagesFromJSONLLines(lines)

	detail, err := replayChatHistory(*sum, lines, rawMessages, s.ChatDir(chatID), runStartedAt, runCompletedAt, runFinishReasons)
	if err != nil {
		return Detail{}, err
	}
	detail.Artifact, err = loadArtifactStateFromManifest(s.ChatDir(chatID), chatID)
	if err != nil {
		return Detail{}, err
	}
	return detail, nil
}

func (s *FileStore) replayRunLifecycleTimesLocked(chatID string) (map[string]int64, map[string]int64, map[string]string, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_, STARTED_AT_, COMPLETED_AT_, FINISH_REASON_ FROM RUNS WHERE CHAT_ID_=?`, chatID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	startedAt := map[string]int64{}
	completedAt := map[string]int64{}
	finishReasons := map[string]string{}
	for rows.Next() {
		var runID string
		var started, completed int64
		var finishReason string
		if err := rows.Scan(&runID, &started, &completed, &finishReason); err != nil {
			return nil, nil, nil, err
		}
		runID = strings.TrimSpace(runID)
		if runID == "" {
			continue
		}
		if err := timecontract.ValidateEpochMillis(started, "startedAt", "chat.replay.runs["+runID+"].startedAt"); err != nil {
			return nil, nil, nil, err
		}
		startedAt[runID] = started
		if completed == 0 {
			if strings.TrimSpace(finishReason) != "" {
				return nil, nil, nil, &timecontract.Violation{Field: "completedAt", Location: "chat.replay.runs[" + runID + "].completedAt", Reason: "is required"}
			}
			continue
		}
		if err := timecontract.ValidateEpochMillis(completed, "completedAt", "chat.replay.runs["+runID+"].completedAt"); err != nil {
			return nil, nil, nil, err
		}
		completedAt[runID] = completed
		finishReasons[runID] = strings.ToLower(strings.TrimSpace(finishReason))
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	return startedAt, completedAt, finishReasons, nil
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
	lines, err = s.logicalHistoryLines(chatID, lines)
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
		case StepLineTypeReact, StepLineTypeReactTool:
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

func firstNonEmptyReplayString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func replayRunLifecycleTimesByRuns(runs []RunSummary, location string) (map[string]int64, map[string]int64, map[string]string, error) {
	startedAt := make(map[string]int64, len(runs))
	completedAt := make(map[string]int64, len(runs))
	finishReasons := make(map[string]string, len(runs))
	for _, run := range runs {
		runID := strings.TrimSpace(run.RunID)
		if runID == "" {
			continue
		}
		if err := timecontract.ValidateEpochMillis(run.StartedAt, "startedAt", location+"["+runID+"].startedAt"); err != nil {
			return nil, nil, nil, err
		}
		startedAt[runID] = run.StartedAt
		if err := timecontract.ValidateEpochMillis(run.CompletedAt, "completedAt", location+"["+runID+"].completedAt"); err != nil {
			return nil, nil, nil, err
		}
		completedAt[runID] = run.CompletedAt
		finishReasons[runID] = strings.ToLower(strings.TrimSpace(run.FinishReason))
	}
	return startedAt, completedAt, finishReasons, nil
}

func replayTerminalEventType(finishReason string) string {
	switch strings.ToLower(strings.TrimSpace(finishReason)) {
	case "error":
		return "run.error"
	case "cancel", "cancelled", "canceled", "interrupted":
		return "run.cancel"
	default:
		return "run.complete"
	}
}

func hasReplayTerminalEvent(events []stream.EventData, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType && strings.TrimSpace(event.String("taskId")) == "" {
			return true
		}
	}
	return false
}

func synthesizedReplayTerminalEvent(runID string, eventType string, timestamp int64, seq int64) stream.EventData {
	payload := map[string]any{"runId": runID}
	switch eventType {
	case "run.error":
		payload["error"] = apperrors.Payload(
			apperrors.CodeRunError,
			"run failed",
			apperrors.WithScope(apperrors.ScopeRun),
			apperrors.WithCategory(apperrors.CategoryChatRun),
		)
	case "run.complete":
		payload["finishReason"] = "stop"
	}
	return stream.EventData{
		Seq:       seq,
		Type:      eventType,
		Timestamp: timestamp,
		Payload:   payload,
	}
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
