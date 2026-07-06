package stream

func (d *StreamEventDispatcher) handleReasoningDelta(input ReasoningDelta) []StreamEvent {
	taskID := d.resolveTaskID(input.TaskID)
	scope := taskScope(taskID)
	events := d.closeForSwitch("reasoning", taskID)
	reasoningLabel := ReasoningLabelForID(input.ReasoningID)
	active, ok := d.state.activeReasonings[scope]
	if !ok || active.ID != input.ReasoningID {
		d.state.activeReasonings[scope] = activeReasoningState{
			ID:    input.ReasoningID,
			Block: reasoningBlockState{TaskID: taskID, Label: reasoningLabel},
		}
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
	if len(d.state.activeReasonings) == 0 {
		return nil
	}
	var events []StreamEvent
	for scope := range d.state.activeReasonings {
		events = append(events, d.closeReasoningScope(scope)...)
	}
	return events
}

func (d *StreamEventDispatcher) closeReasoningScope(scope string) []StreamEvent {
	active, ok := d.state.activeReasonings[scope]
	if !ok {
		return nil
	}
	delete(d.state.activeReasonings, scope)
	reasoningID := active.ID
	block := active.Block
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
