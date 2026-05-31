package chat

import (
	"strings"

	"agent-platform/internal/stream"
)

type stepAwaitingReplay struct {
	items          []map[string]any
	awaitingByTool map[string][]int
	consumed       map[int]bool
}

func newStepAwaitingReplay(rawAwaiting any, runID string) *stepAwaitingReplay {
	awaitingList, _ := rawAwaiting.([]any)
	replay := &stepAwaitingReplay{
		items:          make([]map[string]any, 0, len(awaitingList)),
		awaitingByTool: map[string][]int{},
		consumed:       map[int]bool{},
	}
	for _, rawItem := range awaitingList {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}

		normalized := cloneStringAnyMap(item)
		if _, ok := normalized["runId"]; !ok && runID != "" {
			normalized["runId"] = runID
		}

		idx := len(replay.items)
		replay.items = append(replay.items, normalized)

		itemType, _ := normalized["type"].(string)
		if itemType != "awaiting.ask" {
			continue
		}
		awaitingID, _ := normalized["awaitingId"].(string)
		if awaitingID == "" {
			continue
		}
		replay.awaitingByTool[awaitingID] = append(replay.awaitingByTool[awaitingID], idx)
	}
	return replay
}

func (r *stepAwaitingReplay) consumeForTool(toolID string) []stream.EventData {
	if r == nil || toolID == "" {
		return nil
	}
	indexes := r.awaitingByTool[toolID]
	if len(indexes) == 0 {
		return nil
	}

	events := make([]stream.EventData, 0, len(indexes))
	for _, idx := range indexes {
		if r.consumed[idx] {
			continue
		}
		r.consumed[idx] = true
		events = append(events, stream.EventDataFromMap(r.items[idx]))
	}
	delete(r.awaitingByTool, toolID)
	return events
}

func (r *stepAwaitingReplay) leftoverEvents() []stream.EventData {
	if r == nil || len(r.items) == 0 {
		return nil
	}

	events := make([]stream.EventData, 0, len(r.items))
	for idx, item := range r.items {
		if r.consumed[idx] {
			continue
		}
		events = append(events, stream.EventDataFromMap(item))
	}
	return events
}

func shouldSuppressImmediateAwaitingAskReplay(event map[string]any, fallbackRunID string, stepAwaitingAsks map[string]bool) bool {
	if len(stepAwaitingAsks) == 0 || strings.TrimSpace(stringValue(event["type"])) != "awaiting.ask" {
		return false
	}
	awaitingID := strings.TrimSpace(stringValue(event["awaitingId"]))
	if awaitingID == "" {
		return false
	}
	runID := firstNonBlankReplayString(stringValue(event["runId"]), fallbackRunID)
	return stepAwaitingAsks[awaitingReplayKey(runID, awaitingID)]
}

func awaitingReplayKey(runID string, awaitingID string) string {
	return strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(awaitingID)
}

func firstNonBlankReplayString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
