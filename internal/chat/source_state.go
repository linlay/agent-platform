package chat

import (
	"strings"

	"agent-platform/internal/stream"
)

func sourceItemFromEvent(event stream.EventData) map[string]any {
	item := eventPayloadWithoutSeq(event)
	delete(item, "type")
	if event.Timestamp > 0 {
		item["timestamp"] = event.Timestamp
	}
	if event.Seq > 0 {
		item["liveSeq"] = event.Seq
	}
	return item
}

func appendSourceStateItem(state *SourceState, item map[string]any) *SourceState {
	if len(item) == 0 {
		return state
	}
	if state == nil {
		state = &SourceState{}
	}
	state.Items = append(state.Items, cloneMapDeep(item))
	return state
}

func cloneSourceState(state *SourceState) *SourceState {
	if state == nil || len(state.Items) == 0 {
		return nil
	}
	out := &SourceState{Items: make([]map[string]any, 0, len(state.Items))}
	for _, item := range state.Items {
		if len(item) == 0 {
			continue
		}
		out.Items = append(out.Items, cloneMapDeep(item))
	}
	if len(out.Items) == 0 {
		return nil
	}
	return out
}

func sourceStateItems(raw any) []map[string]any {
	switch typed := raw.(type) {
	case *SourceState:
		if typed == nil {
			return nil
		}
		return cloneSourceItems(typed.Items)
	case SourceState:
		return cloneSourceItems(typed.Items)
	case map[string]any:
		return cloneSourceItems(toMapSlice(typed["items"]))
	default:
		return nil
	}
}

func cloneSourceItems(items []map[string]any) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if len(item) == 0 {
			continue
		}
		out = append(out, cloneMapDeep(item))
	}
	return out
}

func storedMessagesContainTool(messages []StoredMessage) bool {
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			return true
		}
	}
	return false
}

type stepSourceReplay struct {
	items       []map[string]any
	consumed    []bool
	runID       string
	taskID      string
	lineLiveSeq int64
	lineTS      int64
	nextSeq     func() int64
}

func newStepSourceReplay(raw any, runID, taskID string, lineLiveSeq int64, lineTS int64, nextSeq func() int64) *stepSourceReplay {
	items := sourceStateItems(raw)
	if len(items) == 0 {
		return nil
	}
	return &stepSourceReplay{
		items:       items,
		consumed:    make([]bool, len(items)),
		runID:       runID,
		taskID:      taskID,
		lineLiveSeq: lineLiveSeq,
		lineTS:      lineTS,
		nextSeq:     nextSeq,
	}
}

func (r *stepSourceReplay) consumeForTool(toolID string) []stream.EventData {
	if r == nil || strings.TrimSpace(toolID) == "" {
		return nil
	}
	var events []stream.EventData
	for index, item := range r.items {
		if r.consumed[index] || strings.TrimSpace(stringFromAny(item["toolId"])) != strings.TrimSpace(toolID) {
			continue
		}
		r.consumed[index] = true
		events = append(events, r.eventForItem(item))
	}
	return events
}

func (r *stepSourceReplay) leftoverEvents() []stream.EventData {
	if r == nil {
		return nil
	}
	var events []stream.EventData
	for index, item := range r.items {
		if r.consumed[index] {
			continue
		}
		r.consumed[index] = true
		events = append(events, r.eventForItem(item))
	}
	return events
}

func (r *stepSourceReplay) eventForItem(item map[string]any) stream.EventData {
	payload := cloneMapDeep(item)
	timestamp := int64FromAny(payload["timestamp"])
	if timestamp == 0 {
		timestamp = r.lineTS
	}
	liveSeq := int64FromAny(payload["liveSeq"])
	if liveSeq == 0 {
		liveSeq = r.lineLiveSeq
	}
	delete(payload, "timestamp")
	delete(payload, "liveSeq")
	delete(payload, "type")
	if _, ok := payload["runId"]; !ok && strings.TrimSpace(r.runID) != "" {
		payload["runId"] = r.runID
	}
	if _, ok := payload["taskId"]; !ok && strings.TrimSpace(r.taskID) != "" {
		payload["taskId"] = r.taskID
	}
	addReplayLiveSeq(payload, liveSeq)
	seq := int64(0)
	if r.nextSeq != nil {
		seq = r.nextSeq()
	}
	return stream.EventData{
		Seq:       seq,
		Type:      "source.publish",
		Timestamp: timestamp,
		Payload:   payload,
	}
}
