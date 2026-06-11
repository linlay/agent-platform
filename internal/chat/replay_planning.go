package chat

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/stream"
)

func planningSnapshotFromLine(line map[string]any, chatDir string) (*PlanningState, *stream.EventData) {
	event, _ := line["event"].(map[string]any)
	if len(event) == 0 || strings.TrimSpace(stringFromAny(event["type"])) != "planning.snapshot" {
		return nil, nil
	}

	planningID := strings.TrimSpace(stringFromAny(event["planningId"]))
	planningFile := strings.TrimSpace(stringFromAny(event["planningFile"]))
	markdown := stringFromAny(event["text"])
	if markdown == "" {
		markdown = stringFromAny(event["markdown"])
	}
	state := planningStateFromRef(planningID, planningFile, markdown, chatDir)
	if state == nil {
		return nil, nil
	}

	timestamp := int64FromAny(event["timestamp"])
	if timestamp == 0 {
		timestamp = int64FromAny(line["updatedAt"])
	}
	chatID := strings.TrimSpace(stringFromAny(event["chatId"]))
	if chatID == "" {
		chatID = strings.TrimSpace(stringFromAny(line["chatId"]))
	}
	runID := strings.TrimSpace(stringFromAny(event["runId"]))
	if runID == "" {
		runID = strings.TrimSpace(stringFromAny(line["runId"]))
	}

	payload := map[string]any{
		"planningId":   state.PlanningID,
		"planningFile": state.PlanningFile,
	}
	if chatID != "" {
		payload["chatId"] = chatID
	}
	if runID != "" {
		payload["runId"] = runID
	}
	if state.Markdown != "" {
		payload["text"] = state.Markdown
	}

	eventData := &stream.EventData{
		Type:      "planning.snapshot",
		Timestamp: timestamp,
		Payload:   payload,
	}
	return state, eventData
}

func planningSnapshotFromAwaitingItem(item map[string]any, chatID string, runID string, chatDir string, fallbackTimestamp int64, legacyPlanningSnapshotIDs map[string]bool) (*PlanningState, *stream.EventData) {
	if strings.TrimSpace(stringFromAny(item["type"])) != "awaiting.ask" ||
		!strings.EqualFold(strings.TrimSpace(stringFromAny(item["mode"])), "plan") {
		return nil, nil
	}
	plan, _ := item["plan"].(map[string]any)
	if len(plan) == 0 {
		return nil, nil
	}
	state := planningStateFromRef(
		strings.TrimSpace(stringFromAny(plan["planningId"])),
		strings.TrimSpace(stringFromAny(plan["planningFile"])),
		"",
		chatDir,
	)
	if state == nil {
		return nil, nil
	}
	if legacyPlanningSnapshotIDs != nil && legacyPlanningSnapshotIDs[state.PlanningID] {
		return state, nil
	}
	if state.Markdown == "" {
		return state, nil
	}

	timestamp := int64FromAny(item["timestamp"])
	if timestamp == 0 {
		timestamp = fallbackTimestamp
	}
	if strings.TrimSpace(chatID) == "" {
		chatID = strings.TrimSpace(stringFromAny(item["chatId"]))
	}
	if strings.TrimSpace(runID) == "" {
		runID = strings.TrimSpace(stringFromAny(item["runId"]))
	}

	payload := map[string]any{
		"planningId":   state.PlanningID,
		"planningFile": state.PlanningFile,
		"text":         state.Markdown,
	}
	if strings.TrimSpace(chatID) != "" {
		payload["chatId"] = strings.TrimSpace(chatID)
	}
	if strings.TrimSpace(runID) != "" {
		payload["runId"] = strings.TrimSpace(runID)
	}

	return state, &stream.EventData{
		Type:      "planning.snapshot",
		Timestamp: timestamp,
		Payload:   payload,
	}
}

func planningStateFromAwaitingPlan(rawAwaiting any, chatDir string) *PlanningState {
	var latest *PlanningState
	for _, item := range toMapSlice(rawAwaiting) {
		if strings.TrimSpace(stringFromAny(item["type"])) != "awaiting.ask" ||
			!strings.EqualFold(strings.TrimSpace(stringFromAny(item["mode"])), "plan") {
			continue
		}
		plan, _ := item["plan"].(map[string]any)
		if len(plan) == 0 {
			continue
		}
		state := planningStateFromRef(
			strings.TrimSpace(stringFromAny(plan["planningId"])),
			strings.TrimSpace(stringFromAny(plan["planningFile"])),
			"",
			chatDir,
		)
		if state != nil {
			latest = state
		}
	}
	return latest
}

func legacyPlanningSnapshotIDsFromLines(lines []map[string]any, chatDir string) map[string]bool {
	ids := map[string]bool{}
	for _, line := range lines {
		lineType := strings.TrimSpace(stringFromAny(line["_type"]))
		if lineType != "planning" && lineType != "event" && lineType != "steer" {
			continue
		}
		event, _ := line["event"].(map[string]any)
		if len(event) == 0 || strings.TrimSpace(stringFromAny(event["type"])) != "planning.snapshot" {
			continue
		}
		state, replayedEvent := planningSnapshotFromLine(line, chatDir)
		if state != nil && replayedEvent != nil {
			ids[state.PlanningID] = true
		}
	}
	return ids
}

func planningStateFromRef(planningID string, planningFile string, markdown string, chatDir string) *PlanningState {
	planningID = planningIDFromRef(planningID, planningFile)
	planningFile = strings.TrimSpace(planningFile)
	if planningID == "" {
		return nil
	}

	resolvedFile := resolvePlanningFileForReplay(planningFile, chatDir, planningID)
	responseFile := planningFile
	if fileExists(resolvedFile) || responseFile == "" {
		responseFile = resolvedFile
	}
	if strings.TrimSpace(responseFile) == "" {
		return nil
	}

	if markdown == "" && resolvedFile != "" {
		if data, err := os.ReadFile(resolvedFile); err == nil {
			markdown = string(data)
		}
	}

	return &PlanningState{
		PlanningID:   planningID,
		PlanningFile: responseFile,
		Markdown:     markdown,
	}
}

func planningIDFromRef(planningID string, planningFile string) string {
	planningID = strings.TrimSpace(planningID)
	planningFile = strings.TrimSpace(planningFile)
	if planningID == "" && planningFile != "" {
		base := filepath.Base(planningFile)
		planningID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return planningID
}

func resolvePlanningFileForReplay(planningFile string, chatDir string, planningID string) string {
	planningFile = strings.TrimSpace(planningFile)
	chatDir = strings.TrimSpace(chatDir)
	planningID = strings.TrimSpace(planningID)

	candidates := make([]string, 0, 2)
	if planningFile != "" {
		candidates = append(candidates, planningFile)
	}
	if chatDir != "" && planningID != "" {
		candidates = append(candidates, filepath.Join(chatDir, ToolRootDirName, ToolPlansDirName, planningID+".md"))
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
