package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// logicalHistoryLines returns the replayable view of JSONL. The source JSONL
// remains untouched for audit/export. Only the last model group of a failed or
// cancelled run is eligible for legacy repair because older files have no
// explicit model-turn commit record.
func (s *FileStore) logicalHistoryLines(chatID string, lines []map[string]any) ([]map[string]any, error) {
	eligible, err := s.legacyRepairableRunIDs(chatID)
	if err != nil {
		return nil, err
	}
	return filterLegacyIncompleteModelTurnsWithRuns(lines, eligible)
}

func (s *FileStore) legacyRepairableRunIDs(chatID string) (map[string]legacyRepairableRunState, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_, COALESCE(FINISH_REASON_,''), COALESCE(COMPLETED_AT_,0) FROM RUNS WHERE CHAT_ID_=?`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	eligible := map[string]legacyRepairableRunState{}
	for rows.Next() {
		var runID, finishReason string
		var completedAt int64
		if err := rows.Scan(&runID, &finishReason, &completedAt); err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(finishReason)) {
		case "error", "failed", "cancel", "cancelled", "canceled":
			eligible[strings.TrimSpace(runID)] = legacyRepairableRunState{
				finishReason: finishReason,
				completedAt:  completedAt,
			}
		}
	}
	return eligible, rows.Err()
}

func legacyRepairableRunIDsFromSummaries(runs []RunSummary) map[string]legacyRepairableRunState {
	eligible := map[string]legacyRepairableRunState{}
	for _, run := range runs {
		switch strings.ToLower(strings.TrimSpace(run.FinishReason)) {
		case "error", "failed", "cancel", "cancelled", "canceled":
			eligible[strings.TrimSpace(run.RunID)] = legacyRepairableRunState{
				finishReason: run.FinishReason,
				completedAt:  run.CompletedAt,
			}
		}
	}
	return eligible
}

type legacyModelTurnGroup struct {
	runID  string
	taskID string
	seq    int
}

type legacyRepairableRunState struct {
	finishReason string
	completedAt  int64
}

func (r legacyRepairableRunState) cancelled() bool {
	switch strings.ToLower(strings.TrimSpace(r.finishReason)) {
	case "cancel", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

type legacyInterruptedToolCall struct {
	id   string
	name string
}

type legacyInterruptedAwaiting struct {
	id        string
	mode      string
	timestamp int64
	toolIDs   map[string]bool
}

func (g legacyModelTurnGroup) key() string {
	return g.runID + "\x00" + g.taskID + "\x00" + fmt.Sprintf("%d", g.seq)
}

func legacyGroupFromLine(line map[string]any) legacyModelTurnGroup {
	return legacyModelTurnGroup{
		runID:  strings.TrimSpace(stringValue(line["runId"])),
		taskID: strings.TrimSpace(stringValue(line["taskId"])),
		seq:    toIntFromKeys(line, "seq"),
	}
}

func filterLegacyIncompleteModelTurns(lines []map[string]any, eligibleRuns map[string]bool) ([]map[string]any, error) {
	runs := make(map[string]legacyRepairableRunState, len(eligibleRuns))
	for runID, eligible := range eligibleRuns {
		if eligible {
			runs[runID] = legacyRepairableRunState{finishReason: "error"}
		}
	}
	return filterLegacyIncompleteModelTurnsWithRuns(lines, runs)
}

func filterLegacyIncompleteModelTurnsWithRuns(lines []map[string]any, eligibleRuns map[string]legacyRepairableRunState) ([]map[string]any, error) {
	if len(lines) == 0 || len(eligibleRuns) == 0 {
		return lines, nil
	}

	lastReactByScope := map[string]int{}
	resultIDsByGroup := map[string]map[string]bool{}
	for index, line := range lines {
		group := legacyGroupFromLine(line)
		if _, eligible := eligibleRuns[group.runID]; !eligible {
			continue
		}
		if strings.TrimSpace(stringValue(line["_type"])) == StepLineTypeReact {
			lastReactByScope[group.runID+"\x00"+group.taskID] = index
		}
		for _, message := range anyMessageSlice(line["messages"]) {
			if !strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "tool") {
				continue
			}
			toolID := strings.TrimSpace(firstNonEmptyString(
				stringValue(message["tool_call_id"]),
				stringValue(message["toolId"]),
				stringValue(message["tool_id"]),
			))
			if toolID == "" {
				continue
			}
			key := group.key()
			if resultIDsByGroup[key] == nil {
				resultIDsByGroup[key] = map[string]bool{}
			}
			resultIDsByGroup[key][toolID] = true
		}
	}

	dropGroups := map[string]bool{}
	synthesizedAfter := map[int][]map[string]any{}
	for _, index := range lastReactByScope {
		line := lines[index]
		if !stepLineHasAssistantToolCalls(line) {
			continue
		}
		group := legacyGroupFromLine(line)
		if group.seq <= 0 {
			return nil, fmt.Errorf("%w: runId=%s has a terminal tool-call react without a usable seq boundary", ErrChatHistoryIncomplete, group.runID)
		}
		results := resultIDsByGroup[group.key()]
		missing := make([]legacyInterruptedToolCall, 0)
		hasMalformed := false
		for _, message := range anyMessageSlice(line["messages"]) {
			if !strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "assistant") {
				continue
			}
			for _, call := range anyMessageSlice(message["tool_calls"]) {
				toolID, structurallyValid := validateLegacyToolCall(call)
				if !structurallyValid {
					if toolID != "" && results[toolID] {
						return nil, fmt.Errorf("%w: runId=%s seq=%d has a malformed tool call with a matching result; the persisted model turn cannot be safely replayed", ErrChatHistoryIncomplete, group.runID, group.seq)
					}
					dropGroups[group.key()] = true
					hasMalformed = true
					continue
				}
				if results[toolID] {
					continue
				}
				missing = append(missing, legacyInterruptedToolCall{
					id:   toolID,
					name: strings.TrimSpace(stringValue(mapValue(call["function"])["name"])),
				})
			}
		}
		if len(missing) == 0 {
			continue
		}
		run := eligibleRuns[group.runID]
		if hasMalformed || len(results) > 0 || !run.cancelled() {
			return nil, fmt.Errorf("%w: runId=%s seq=%d has a valid tool call without a matching result; tool execution state is unknown", ErrChatHistoryIncomplete, group.runID, group.seq)
		}
		synthetic, ok := synthesizeLegacyInterruptedAwaiting(lines, line, group, run, missing)
		if !ok {
			return nil, fmt.Errorf("%w: runId=%s seq=%d has a valid tool call without a matching result; tool execution state is unknown", ErrChatHistoryIncomplete, group.runID, group.seq)
		}
		synthesizedAfter[index] = synthetic
	}
	if len(dropGroups) == 0 && len(synthesizedAfter) == 0 {
		return lines, nil
	}
	filtered := make([]map[string]any, 0, len(lines)+(len(synthesizedAfter)*2))
	for index, line := range lines {
		lineType := strings.TrimSpace(stringValue(line["_type"]))
		if (lineType == StepLineTypeReact || lineType == StepLineTypeReactTool) && dropGroups[legacyGroupFromLine(line).key()] {
			continue
		}
		filtered = append(filtered, line)
		filtered = append(filtered, synthesizedAfter[index]...)
	}
	return filtered, nil
}

func synthesizeLegacyInterruptedAwaiting(
	lines []map[string]any,
	line map[string]any,
	group legacyModelTurnGroup,
	run legacyRepairableRunState,
	missing []legacyInterruptedToolCall,
) ([]map[string]any, bool) {
	if run.completedAt <= 0 || len(missing) == 0 {
		return nil, false
	}
	awaitings := legacyInterruptedAwaitings(line["awaiting"])
	if len(awaitings) == 0 {
		return nil, false
	}

	matchedAwaiting := make(map[string]legacyInterruptedAwaiting, len(awaitings))
	for _, call := range missing {
		var matches []legacyInterruptedAwaiting
		for _, awaiting := range awaitings {
			if awaiting.id == call.id || awaiting.toolIDs[call.id] {
				matches = append(matches, awaiting)
			}
		}
		if len(matches) == 0 && len(missing) == 1 && len(awaitings) == 1 {
			matches = append(matches, awaitings[0])
		}
		if len(matches) != 1 {
			return nil, false
		}
		matchedAwaiting[matches[0].id] = matches[0]
	}
	if len(matchedAwaiting) != 1 || len(awaitings) != 1 {
		return nil, false
	}
	for _, persisted := range lines {
		if strings.TrimSpace(stringValue(persisted["runId"])) != group.runID {
			continue
		}
		submit := mapValue(persisted["submit"])
		answer := mapValue(persisted["answer"])
		event := mapValue(persisted["event"])
		for awaitingID := range matchedAwaiting {
			if submitLineMatchesAwaiting(submit, answer, awaitingID) ||
				isPersistedAwaitingAnswer(event, awaitingID) {
				return nil, false
			}
		}
	}

	awaitingOrder := make([]legacyInterruptedAwaiting, 0, len(matchedAwaiting))
	for _, awaiting := range awaitings {
		if _, ok := matchedAwaiting[awaiting.id]; ok {
			awaitingOrder = append(awaitingOrder, awaiting)
		}
	}
	synthetic := make([]map[string]any, 0, len(awaitingOrder)+1)
	durationByAwaiting := make(map[string]int64, len(awaitingOrder))
	for _, awaiting := range awaitingOrder {
		startedAt := awaiting.timestamp
		if startedAt <= 0 {
			startedAt = int64FromAny(line["updatedAt"])
		}
		duration := run.completedAt - startedAt
		if duration < 0 {
			duration = 0
		}
		durationByAwaiting[awaiting.id] = duration
		synthetic = append(synthetic, map[string]any{
			"chatId":    stringValue(line["chatId"]),
			"runId":     group.runID,
			"updatedAt": run.completedAt,
			"answer": map[string]any{
				"awaitingId": awaiting.id,
				"mode":       awaiting.mode,
				"status":     "error",
				"durationMs": duration,
				"error": map[string]any{
					"code":    "run_interrupted",
					"message": "run interrupted while waiting for user approval",
					"reason":  "run_interrupted",
				},
			},
			"_type": "submit",
		})
	}

	toolMessages := make([]map[string]any, 0, len(missing))
	for _, call := range missing {
		awaiting, ok := legacyAwaitingForTool(call.id, missing, awaitings, matchedAwaiting)
		if !ok {
			return nil, false
		}
		output := "tool execution was cancelled before execution"
		if strings.EqualFold(awaiting.mode, "approval") || strings.EqualFold(awaiting.mode, "form") {
			output = "tool execution was cancelled before approval"
		}
		payload, err := json.Marshal(map[string]any{
			"error":      "run_interrupted",
			"exitCode":   -1,
			"output":     output,
			"executed":   false,
			"awaitingId": awaiting.id,
		})
		if err != nil {
			return nil, false
		}
		toolMessages = append(toolMessages, map[string]any{
			"role": "tool",
			"content": []any{map[string]any{
				"type": "text",
				"text": string(payload),
			}},
			"name":         call.name,
			"tool_call_id": call.id,
			"durationMs":   durationByAwaiting[awaiting.id],
			"ts":           run.completedAt,
			"_toolId":      call.id,
		})
	}
	toolLine := map[string]any{
		"chatId":    stringValue(line["chatId"]),
		"runId":     group.runID,
		"updatedAt": run.completedAt,
		"messages":  mapSliceToAny(toolMessages),
		"_type":     StepLineTypeReactTool,
		"seq":       group.seq,
	}
	for _, key := range []string{"taskId", "taskStatus", "taskSubAgentKey", "teamId", "presentation", "stage"} {
		if value, ok := line[key]; ok {
			toolLine[key] = value
		}
	}
	synthetic = append(synthetic, toolLine)
	return synthetic, true
}

func mapSliceToAny(items []map[string]any) []any {
	result := make([]any, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}

func legacyInterruptedAwaitings(raw any) []legacyInterruptedAwaiting {
	items := toMapSlice(raw)
	result := make([]legacyInterruptedAwaiting, 0, len(items))
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(stringValue(item["type"])), "awaiting.ask") {
			continue
		}
		awaitingID := strings.TrimSpace(stringValue(item["awaitingId"]))
		if awaitingID == "" {
			continue
		}
		toolIDs := map[string]bool{}
		for _, key := range []string{"approvals", "forms"} {
			for _, candidate := range anyMessageSlice(item[key]) {
				if id := strings.TrimSpace(stringValue(candidate["id"])); id != "" {
					toolIDs[id] = true
				}
			}
		}
		result = append(result, legacyInterruptedAwaiting{
			id:        awaitingID,
			mode:      strings.TrimSpace(stringValue(item["mode"])),
			timestamp: int64FromAny(item["timestamp"]),
			toolIDs:   toolIDs,
		})
	}
	return result
}

func legacyAwaitingForTool(
	toolID string,
	missing []legacyInterruptedToolCall,
	awaitings []legacyInterruptedAwaiting,
	matched map[string]legacyInterruptedAwaiting,
) (legacyInterruptedAwaiting, bool) {
	for _, awaiting := range awaitings {
		if _, ok := matched[awaiting.id]; ok && (awaiting.id == toolID || awaiting.toolIDs[toolID]) {
			return awaiting, true
		}
	}
	if len(missing) == 1 && len(matched) == 1 {
		for _, awaiting := range matched {
			return awaiting, true
		}
	}
	return legacyInterruptedAwaiting{}, false
}

func stepLineHasAssistantToolCalls(line map[string]any) bool {
	for _, message := range anyMessageSlice(line["messages"]) {
		if strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "assistant") &&
			len(anyMessageSlice(message["tool_calls"])) > 0 {
			return true
		}
	}
	return false
}

func validateLegacyToolCall(call map[string]any) (string, bool) {
	toolID := strings.TrimSpace(firstNonEmptyString(
		stringValue(call["id"]),
		stringValue(call["toolId"]),
		stringValue(call["tool_id"]),
	))
	if toolID == "" {
		return "", false
	}
	toolType := strings.TrimSpace(stringValue(call["type"]))
	if toolType != "" && !strings.EqualFold(toolType, "function") {
		return toolID, false
	}
	function := mapValue(call["function"])
	if strings.TrimSpace(stringValue(function["name"])) == "" {
		return toolID, false
	}
	switch arguments := function["arguments"].(type) {
	case string:
		if strings.TrimSpace(arguments) == "" {
			return toolID, true
		}
		var decoded map[string]any
		if json.Unmarshal([]byte(arguments), &decoded) != nil {
			return toolID, false
		}
	case map[string]any:
		if arguments == nil {
			return toolID, false
		}
	default:
		return toolID, false
	}
	return toolID, true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
