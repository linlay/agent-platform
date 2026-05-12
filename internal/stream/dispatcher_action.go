package stream

import "strings"

func (d *StreamEventDispatcher) handleActionArgs(input ActionArgs) []StreamEvent {
	events := d.closeForSwitch("action")
	taskID := input.TaskID
	if strings.TrimSpace(taskID) == "" && d.state.activeTaskID != "" {
		taskID = d.state.activeTaskID
	}
	if _, ok := d.state.openActions[input.ActionID]; !ok {
		d.state.openActions[input.ActionID] = actionBlockState{
			TaskID:      taskID,
			Name:        input.ActionName,
			Description: input.Description,
		}
		events = append(events, NewEvent("action.start", map[string]any{
			"actionId":    input.ActionID,
			"runId":       d.request.RunID,
			"taskId":      taskID,
			"actionName":  input.ActionName,
			"description": input.Description,
		}))
	}
	d.state.actionArgsBuffer[input.ActionID] += input.Delta
	events = append(events, NewEvent("action.args", map[string]any{
		"actionId": input.ActionID,
		"delta":    input.Delta,
	}))
	return events
}

func (d *StreamEventDispatcher) handleActionEnd(input ActionEnd) []StreamEvent {
	return d.closeAction(input.ActionID)
}

func (d *StreamEventDispatcher) handleActionResult(input ActionResult) []StreamEvent {
	events := d.closeAction(input.ActionID)
	events = append(events, NewEvent("action.result", map[string]any{
		"actionId": input.ActionID,
		"result":   input.Result,
	}))
	return events
}

func (d *StreamEventDispatcher) closeAllActions() []StreamEvent {
	if len(d.state.openActions) == 0 {
		return nil
	}
	var events []StreamEvent
	for actionID := range d.state.openActions {
		events = append(events, d.closeAction(actionID)...)
	}
	return events
}

func (d *StreamEventDispatcher) closeAction(actionID string) []StreamEvent {
	block, ok := d.state.openActions[actionID]
	if !ok {
		return nil
	}
	delete(d.state.openActions, actionID)
	return []StreamEvent{
		NewEvent("action.end", map[string]any{
			"actionId": actionID,
		}),
		NewEvent("action.snapshot", map[string]any{
			"actionId":    actionID,
			"runId":       d.request.RunID,
			"actionName":  block.Name,
			"taskId":      block.TaskID,
			"description": block.Description,
			"arguments":   d.state.actionArgsBuffer[actionID],
		}),
	}
}
