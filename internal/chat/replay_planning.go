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

func planningStateFromRef(planningID string, planningFile string, markdown string, chatDir string) *PlanningState {
	planningID = strings.TrimSpace(planningID)
	planningFile = strings.TrimSpace(planningFile)
	if planningID == "" && planningFile != "" {
		base := filepath.Base(planningFile)
		planningID = strings.TrimSuffix(base, filepath.Ext(base))
	}
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
