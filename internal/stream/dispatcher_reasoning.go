package stream

import "strings"

func (d *StreamEventDispatcher) handleReasoningDelta(input ReasoningDelta) []StreamEvent {
	events := d.closeForSwitch("reasoning")
	taskID := input.TaskID
	if strings.TrimSpace(taskID) == "" && d.state.activeTaskID != "" {
		taskID = d.state.activeTaskID
	}
	reasoningLabel := ReasoningLabelForID(input.ReasoningID)
	if d.state.activeReasoningID == "" || d.state.activeReasoningID != input.ReasoningID {
		d.state.activeReasoningID = input.ReasoningID
		d.state.activeReasoning = reasoningBlockState{TaskID: taskID, Label: reasoningLabel}
		events = append(events, NewEvent("reasoning.start", map[string]any{
			"runId":          d.request.RunID,
			"reasoningId":    input.ReasoningID,
			"taskId":         taskID,
			"reasoningLabel": reasoningLabel,
		}))
	}
	d.state.reasoningBuffer[input.ReasoningID] += input.Delta
	d.state.reasoningSeen = true
	d.state.lastReasoningID = input.ReasoningID
	d.state.fullReasoning += input.Delta
	events = append(events, NewEvent("reasoning.delta", map[string]any{
		"reasoningId": input.ReasoningID,
		"delta":       input.Delta,
	}))
	return events
}

func (d *StreamEventDispatcher) closeReasoning() []StreamEvent {
	if d.state.activeReasoningID == "" {
		return nil
	}
	reasoningID := d.state.activeReasoningID
	block := d.state.activeReasoning
	d.state.activeReasoningID = ""
	d.state.activeReasoning = reasoningBlockState{}
	events := []StreamEvent{NewEvent("reasoning.end", map[string]any{
		"reasoningId": reasoningID,
	})}
	if d.state.reasoningSeen {
		events = append(events, NewEvent("reasoning.snapshot", map[string]any{
			"reasoningId":    reasoningID,
			"runId":          d.request.RunID,
			"text":           d.state.reasoningBuffer[reasoningID],
			"taskId":         block.TaskID,
			"reasoningLabel": block.Label,
		}))
	}
	return events
}
