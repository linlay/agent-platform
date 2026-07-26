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
	return filterLegacyIncompleteModelTurns(lines, eligible)
}

func (s *FileStore) legacyRepairableRunIDs(chatID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_, COALESCE(FINISH_REASON_,'') FROM RUNS WHERE CHAT_ID_=?`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	eligible := map[string]bool{}
	for rows.Next() {
		var runID, finishReason string
		if err := rows.Scan(&runID, &finishReason); err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(finishReason)) {
		case "error", "failed", "cancel", "cancelled", "canceled":
			eligible[strings.TrimSpace(runID)] = true
		}
	}
	return eligible, rows.Err()
}

func legacyRepairableRunIDsFromSummaries(runs []RunSummary) map[string]bool {
	eligible := map[string]bool{}
	for _, run := range runs {
		switch strings.ToLower(strings.TrimSpace(run.FinishReason)) {
		case "error", "failed", "cancel", "cancelled", "canceled":
			eligible[strings.TrimSpace(run.RunID)] = true
		}
	}
	return eligible
}

type legacyModelTurnGroup struct {
	runID  string
	taskID string
	seq    int
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
	if len(lines) == 0 || len(eligibleRuns) == 0 {
		return lines, nil
	}

	lastReactByScope := map[string]int{}
	resultIDsByGroup := map[string]map[string]bool{}
	for index, line := range lines {
		group := legacyGroupFromLine(line)
		if !eligibleRuns[group.runID] {
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
					continue
				}
				if results[toolID] {
					continue
				}
				return nil, fmt.Errorf("%w: runId=%s seq=%d has a valid tool call without a matching result; tool execution state is unknown", ErrChatHistoryIncomplete, group.runID, group.seq)
			}
		}
	}
	if len(dropGroups) == 0 {
		return lines, nil
	}
	filtered := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		lineType := strings.TrimSpace(stringValue(line["_type"]))
		if (lineType == StepLineTypeReact || lineType == StepLineTypeReactTool) && dropGroups[legacyGroupFromLine(line).key()] {
			continue
		}
		filtered = append(filtered, line)
	}
	return filtered, nil
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
