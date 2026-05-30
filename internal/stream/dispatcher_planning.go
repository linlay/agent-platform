package stream

import (
	"strings"
)

func (d *StreamEventDispatcher) handlePlanningDelta(input PlanningDelta) []StreamEvent {
	return []StreamEvent{NewEvent("planning.delta", map[string]any{
		"planningId": strings.TrimSpace(input.PlanningID),
		"delta":      input.Delta,
	})}
}
