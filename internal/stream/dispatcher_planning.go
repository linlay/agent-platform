package stream

import (
	"path/filepath"
	"strings"
	"time"
)

func (d *StreamEventDispatcher) handlePlanningStart(input PlanningStart) []StreamEvent {
	events := d.closeOpenBlocks()
	payload := d.planningContextPayload(input.PlanningID, input.PlanningFile, input.ChatID, input.RunID, input.Title)
	events = append(events, NewEvent("planning.start", payload))
	return events
}

func (d *StreamEventDispatcher) handlePlanningDelta(input PlanningDelta) []StreamEvent {
	return []StreamEvent{NewEvent("planning.delta", map[string]any{
		"planningId": strings.TrimSpace(input.PlanningID),
		"delta":      input.Delta,
	})}
}

func (d *StreamEventDispatcher) handlePlanningSnapshot(input PlanningSnapshot) []StreamEvent {
	payload := d.planningContextPayload(input.PlanningID, input.PlanningFile, input.ChatID, input.RunID, input.Title)
	payload["text"] = input.Markdown
	return []StreamEvent{NewEvent("planning.snapshot", payload)}
}

func (d *StreamEventDispatcher) handlePlanningEnd(input PlanningEnd) []StreamEvent {
	return []StreamEvent{NewEvent("planning.end", map[string]any{
		"planningId": strings.TrimSpace(input.PlanningID),
	})}
}

func (d *StreamEventDispatcher) planningContextPayload(planningID string, planningFile string, chatID string, runID string, title string) map[string]any {
	if strings.TrimSpace(chatID) == "" {
		chatID = d.request.ChatID
	}
	if strings.TrimSpace(runID) == "" {
		runID = d.request.RunID
	}
	return map[string]any{
		"planningId":   strings.TrimSpace(planningID),
		"planningFile": planningFileName(planningFile),
		"chatId":       strings.TrimSpace(chatID),
		"runId":        strings.TrimSpace(runID),
		"title":        strings.TrimSpace(title),
		"updatedAt":    time.Now().UnixMilli(),
	}
}

func planningFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Base(value)
}
