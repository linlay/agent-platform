package stream

import (
	"strings"
)

func (d *StreamEventDispatcher) handlePlanningStart(input PlanningStart) []StreamEvent {
	return []StreamEvent{NewEvent("planning.start", map[string]any{
		"planningId": strings.TrimSpace(input.PlanningID),
	})}
}

func (d *StreamEventDispatcher) handlePlanningDelta(input PlanningDelta) []StreamEvent {
	return []StreamEvent{NewEvent("planning.delta", map[string]any{
		"planningId": strings.TrimSpace(input.PlanningID),
		"delta":      input.Delta,
	})}
}

func (d *StreamEventDispatcher) handlePlanningEnd(input PlanningEnd) []StreamEvent {
	return []StreamEvent{NewEvent("planning.end", map[string]any{
		"planningId": strings.TrimSpace(input.PlanningID),
	})}
}
