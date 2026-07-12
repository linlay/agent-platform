package chat

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

// PlanningSnapshotFromAwaitingItem builds planning state and a replay/live snapshot
// from a persisted or proxied planning awaiting event.
func PlanningSnapshotFromAwaitingItem(item map[string]any, chatID string, runID string, chatDir string) (*PlanningState, *stream.EventData) {
	return planningSnapshotFromAwaitingItem(item, chatID, runID, chatDir)
}

func planningSnapshotFromAwaitingItem(item map[string]any, chatID string, runID string, chatDir string) (*PlanningState, *stream.EventData) {
	if strings.TrimSpace(stringFromAny(item["type"])) != "awaiting.ask" ||
		!strings.EqualFold(strings.TrimSpace(stringFromAny(item["mode"])), "planning") {
		return nil, nil
	}
	planning, _ := item["planning"].(map[string]any)
	if len(planning) == 0 {
		return nil, nil
	}
	state := planningStateFromPlanning(planning, chatDir)
	if state == nil {
		return nil, nil
	}
	if state.Markdown == "" {
		return state, nil
	}

	timestamp, err := timecontract.ParseEpochMillis(item["timestamp"], "timestamp", "chat.awaiting.planning")
	// Public replay paths validate persisted awaiting items before reaching
	// this helper. Keep the defensive branch non-repairing for direct callers:
	// a missing source timestamp must never inherit a line timestamp.
	if err != nil {
		return state, nil
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

func planningStateFromAwaitingPlanning(rawAwaiting any, chatDir string) *PlanningState {
	var latest *PlanningState
	for _, item := range toMapSlice(rawAwaiting) {
		if strings.TrimSpace(stringFromAny(item["type"])) != "awaiting.ask" ||
			!strings.EqualFold(strings.TrimSpace(stringFromAny(item["mode"])), "planning") {
			continue
		}
		planning, _ := item["planning"].(map[string]any)
		if len(planning) == 0 {
			continue
		}
		state := planningStateFromPlanning(planning, chatDir)
		if state != nil {
			latest = state
		}
	}
	return latest
}

func planningStateFromPlanning(planning map[string]any, chatDir string) *PlanningState {
	if len(planning) == 0 {
		return nil
	}
	planningID := strings.TrimSpace(stringFromAny(planning["planningId"]))
	if planningID == "" {
		planningID = strings.TrimSpace(stringFromAny(planning["id"]))
	}
	return planningStateFromRef(
		planningID,
		strings.TrimSpace(stringFromAny(planning["planningFile"])),
		stringFromAny(planning["text"]),
		chatDir,
	)
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
		candidates = append(candidates, filepath.Join(chatDir, ToolRootDirName, ToolPlanningDirName, planningID+".md"))
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

func containsLegacyPlanningAwaiting(lines []map[string]any) bool {
	for _, line := range lines {
		for _, item := range toMapSlice(line["awaiting"]) {
			if strings.EqualFold(strings.TrimSpace(stringFromAny(item["mode"])), "plan") {
				return true
			}
		}
		event, _ := line["event"].(map[string]any)
		if strings.TrimSpace(stringFromAny(event["type"])) == "awaiting.ask" &&
			strings.EqualFold(strings.TrimSpace(stringFromAny(event["mode"])), "plan") {
			return true
		}
	}
	return false
}
