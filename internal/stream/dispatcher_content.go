package stream

import "strings"

func (d *StreamEventDispatcher) handleContentDelta(input ContentDelta) []StreamEvent {
	events := d.closeForSwitch("content")
	taskID := input.TaskID
	if strings.TrimSpace(taskID) == "" && d.state.activeTaskID != "" {
		taskID = d.state.activeTaskID
	}
	if d.state.activeContentID == "" || d.state.activeContentID != input.ContentID {
		d.state.activeContentID = input.ContentID
		d.state.activeContent = contentBlockState{TaskID: taskID}
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
	if d.state.activeContentID == "" {
		return nil
	}
	contentID := d.state.activeContentID
	block := d.state.activeContent
	d.state.activeContentID = ""
	d.state.activeContent = contentBlockState{}
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
