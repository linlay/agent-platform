package chat

import (
	"fmt"

	"agent-platform/internal/stream"
)

type stepAwaitingReplay struct {
	items                   []stream.EventData
	planningSnapshotByIndex map[int]*stream.EventData
	awaitingByTool          map[string][]int
	consumed                map[int]bool
}

func newStepAwaitingReplay(rawAwaiting any, chatID string, runID string, chatDir string, liveSeq int64) (*stepAwaitingReplay, error) {
	awaitingList := toMapSlice(rawAwaiting)
	replay := &stepAwaitingReplay{
		items:                   make([]stream.EventData, 0, len(awaitingList)),
		planningSnapshotByIndex: map[int]*stream.EventData{},
		awaitingByTool:          map[string][]int{},
		consumed:                map[int]bool{},
	}
	for _, item := range awaitingList {
		if item == nil {
			continue
		}

		normalized := cloneStringAnyMap(item)
		clearReplayCursorFields(normalized)
		if _, ok := normalized["runId"]; !ok && runID != "" {
			normalized["runId"] = runID
		}
		addReplayLiveSeq(normalized, liveSeq)
		event, err := stream.ParseEventDataMap(normalized, fmt.Sprintf("chat.jsonl.awaiting[%d]", len(replay.items)))
		if err != nil {
			return nil, err
		}

		idx := len(replay.items)
		replay.items = append(replay.items, event)
		if _, event := planningSnapshotFromAwaitingItem(normalized, chatID, runID, chatDir); event != nil {
			replay.planningSnapshotByIndex[idx] = event
		}

		if event.Type != "awaiting.ask" {
			continue
		}
		awaitingID := event.String("awaitingId")
		if awaitingID == "" {
			continue
		}
		replay.awaitingByTool[awaitingID] = append(replay.awaitingByTool[awaitingID], idx)
	}
	return replay, nil
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
		events = append(events, r.eventsForItem(idx)...)
	}
	delete(r.awaitingByTool, toolID)
	return events
}

func (r *stepAwaitingReplay) leftoverEvents() []stream.EventData {
	if r == nil || len(r.items) == 0 {
		return nil
	}

	events := make([]stream.EventData, 0, len(r.items))
	for idx := range r.items {
		if r.consumed[idx] {
			continue
		}
		events = append(events, r.eventsForItem(idx)...)
	}
	return events
}

func (r *stepAwaitingReplay) eventsForItem(idx int) []stream.EventData {
	if r == nil || idx < 0 || idx >= len(r.items) {
		return nil
	}
	events := make([]stream.EventData, 0, 2)
	if snapshot := r.planningSnapshotByIndex[idx]; snapshot != nil {
		events = append(events, *snapshot)
	}
	events = append(events, r.items[idx])
	return events
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
