package stream

func (d *StreamEventDispatcher) handleContentDelta(input ContentDelta) []StreamEvent {
	taskID := d.resolveTaskID(input.TaskID)
	scope := taskScope(taskID)
	events := d.closeForSwitch("content", taskID)
	active, ok := d.state.activeContents[scope]
	if !ok || active.ID != input.ContentID {
		d.state.activeContents[scope] = activeContentState{
			ID:    input.ContentID,
			Block: contentBlockState{TaskID: taskID},
		}
		d.state.lastContentID = input.ContentID
		events = append(events, NewEvent("content.start", map[string]any{
			"contentId": input.ContentID,
			"runId":     d.request.RunID,
			"taskId":    taskID,
		}))
	}
	d.state.contentSeen = true
	d.state.lastContentID = input.ContentID
	d.state.contentBuffer[input.ContentID] += input.Delta
	d.state.fullContent += input.Delta
	events = append(events, NewEvent("content.delta", map[string]any{
		"contentId": input.ContentID,
		"delta":     input.Delta,
	}))
	return events
}

func (d *StreamEventDispatcher) closeContent() []StreamEvent {
	if len(d.state.activeContents) == 0 {
		return nil
	}
	var events []StreamEvent
	for scope := range d.state.activeContents {
		events = append(events, d.closeContentScope(scope)...)
	}
	return events
}

func (d *StreamEventDispatcher) closeContentScope(scope string) []StreamEvent {
	active, ok := d.state.activeContents[scope]
	if !ok {
		return nil
	}
	delete(d.state.activeContents, scope)
	contentID := active.ID
	block := active.Block
	events := []StreamEvent{NewEvent("content.end", map[string]any{
		"contentId": contentID,
	})}
	if d.state.contentSeen {
		events = append(events, NewEvent("content.snapshot", map[string]any{
			"contentId": contentID,
			"runId":     d.request.RunID,
			"text":      d.state.contentBuffer[contentID],
			"taskId":    block.TaskID,
		}))
	}
	return events
}
