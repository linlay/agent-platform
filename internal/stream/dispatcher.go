package stream

import "strings"

type StreamEventDispatcher struct {
	request StreamRequest
	state   *StreamEventStateData
}

func NewDispatcher(request StreamRequest) *StreamEventDispatcher {
	return &StreamEventDispatcher{
		request: request,
		state:   NewStateData(),
	}
}

func (d *StreamEventDispatcher) Dispatch(input StreamInput) []StreamEvent {
	if d.state.terminated {
		return nil
	}

	switch value := input.(type) {
	case ReasoningDelta:
		return d.handleReasoningDelta(value)
	case ContentDelta:
		return d.handleContentDelta(value)
	case ToolArgs:
		return d.handleToolArgs(value)
	case ToolEnd:
		return d.handleToolEnd(value)
	case ToolResult:
		return d.handleToolResult(value)
	case ActionArgs:
		return d.handleActionArgs(value)
	case ActionEnd:
		return d.handleActionEnd(value)
	case ActionResult:
		return d.handleActionResult(value)
	case StageMarker:
		return []StreamEvent{NewEvent("stage.marker", map[string]any{
			"runId":  d.request.RunID,
			"chatId": d.request.ChatID,
			"stage":  value.Stage,
		})}
	case PlanUpdate:
		return d.handlePlanUpdate(value)
	case TaskStart:
		return d.handleTaskStart(value)
	case TaskComplete:
		return d.handleTaskComplete(value)
	case TaskCancel:
		return d.handleTaskCancel(value)
	case TaskFail:
		return d.handleTaskFail(value)
	case ArtifactPublish:
		return []StreamEvent{NewEvent("artifact.publish", map[string]any{
			"artifactId": value.ArtifactID,
			"chatId":     value.ChatID,
			"runId":      value.RunID,
			"artifact":   value.Artifact,
		})}
	case AwaitQuestion:
		payload := map[string]any{
			"awaitId":      value.AwaitID,
			"awaitName":    value.AwaitName,
			"viewportType": value.ViewportType,
			"viewportKey":  value.ViewportKey,
			"mode":         value.Mode,
			"toolTimeout":  value.ToolTimeout,
			"runId":        value.RunID,
			"chatId":       value.ChatID,
		}
		if value.Payload != nil {
			payload["payload"] = value.Payload
		}
		return []StreamEvent{NewEvent("await.question", payload)}
	case AwaitPayload:
		return []StreamEvent{NewEvent("await.payload", map[string]any{
			"awaitId": value.AwaitID,
			"payload": value.Payload,
		})}
	case AwaitAnswer:
		return []StreamEvent{NewEvent("await.answer", map[string]any{
			"requestId": value.RequestID,
			"chatId":    value.ChatID,
			"runId":     value.RunID,
			"toolId":    value.ToolID,
			"payload":   value.Payload,
		})}
	case RequestSteer:
		events := d.closeOpenBlocks()
		events = append(events, NewEvent("request.steer", map[string]any{
			"requestId": value.RequestID,
			"chatId":    value.ChatID,
			"runId":     value.RunID,
			"steerId":   value.SteerID,
			"message":   value.Message,
			"role":      "user",
		}))
		return events
	case RunCancel:
		events := d.closeOpenBlocks()
		events = append(events, NewEvent("run.cancel", map[string]any{
			"runId": value.RunID,
		}))
		d.state.terminated = true
		return events
	case InputRunComplete:
		d.state.runFinishReason = value.FinishReason
		return nil
	case InputRunError:
		events := d.closeOpenBlocks()
		events = append(events, NewEvent("run.error", map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(value.Error, "run_error", "run", "runtime"),
		}))
		d.state.terminated = true
		return events
	default:
		return nil
	}
}

func (d *StreamEventDispatcher) Complete() []StreamEvent {
	if d.state.terminated {
		return nil
	}
	events := d.closeOpenBlocks()
	if d.state.runError != nil {
		events = append(events, NewEvent("run.error", map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(d.state.runError, "stream_failed", "run", "runtime"),
		}))
		d.state.terminated = true
		return events
	}
	events = append(events, NewEvent("run.complete", map[string]any{
		"runId":        d.request.RunID,
		"finishReason": d.state.runFinishReason,
	}))
	d.state.terminated = true
	return events
}

func (d *StreamEventDispatcher) Fail(err error) []StreamEvent {
	if d.state.terminated {
		return nil
	}
	d.state.runError = map[string]any{
		"code":     "stream_failed",
		"message":  err.Error(),
		"scope":    "run",
		"category": "runtime",
	}
	events := d.closeOpenBlocks()
	events = append(events, NewEvent("run.error", map[string]any{
		"runId": d.request.RunID,
		"error": normalizeErrorMap(d.state.runError, "stream_failed", "run", "runtime"),
	}))
	d.state.terminated = true
	return events
}

func (d *StreamEventDispatcher) handleReasoningDelta(input ReasoningDelta) []StreamEvent {
	events := d.closeForSwitch("reasoning")
	reasoningLabel := strings.TrimSpace(input.ReasoningLabel)
	if reasoningLabel == "" {
		reasoningLabel = ReasoningLabelForID(input.ReasoningID)
	}
	if d.state.activeReasoningID == "" || d.state.activeReasoningID != input.ReasoningID {
		d.state.activeReasoningID = input.ReasoningID
		d.state.activeReasoning = reasoningBlockState{TaskID: input.TaskID, Label: reasoningLabel}
		events = append(events, NewEvent("reasoning.start", map[string]any{
			"runId":          d.request.RunID,
			"reasoningId":    input.ReasoningID,
			"taskId":         input.TaskID,
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

func (d *StreamEventDispatcher) handleContentDelta(input ContentDelta) []StreamEvent {
	events := d.closeForSwitch("content")
	if d.state.activeContentID == "" || d.state.activeContentID != input.ContentID {
		d.state.activeContentID = input.ContentID
		d.state.activeContent = contentBlockState{TaskID: input.TaskID}
		d.state.lastContentID = input.ContentID
		events = append(events, NewEvent("content.start", map[string]any{
			"contentId": input.ContentID,
			"runId":     d.request.RunID,
			"taskId":    input.TaskID,
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

func (d *StreamEventDispatcher) handleToolArgs(input ToolArgs) []StreamEvent {
	events := d.closeForSwitch("tool")
	if _, ok := d.state.openTools[input.ToolID]; !ok {
		d.state.openTools[input.ToolID] = toolBlockState{
			TaskID:      input.TaskID,
			Name:        input.ToolName,
			Type:        input.ToolType,
			Label:       input.ToolLabel,
			Description: input.ToolDescription,
		}
		events = append(events, NewEvent("tool.start", map[string]any{
			"toolId":          input.ToolID,
			"runId":           d.request.RunID,
			"taskId":          input.TaskID,
			"toolName":        input.ToolName,
			"toolType":        input.ToolType,
			"toolLabel":       input.ToolLabel,
			"toolDescription": input.ToolDescription,
		}))
	}
	d.state.toolArgsBuffer[input.ToolID] += input.Delta
	events = append(events, NewEvent("tool.args", map[string]any{
		"toolId":     input.ToolID,
		"delta":      input.Delta,
		"chunkIndex": input.ChunkIndex,
	}))
	return events
}

func (d *StreamEventDispatcher) handleToolEnd(input ToolEnd) []StreamEvent {
	return d.closeTool(input.ToolID)
}

func (d *StreamEventDispatcher) handleToolResult(input ToolResult) []StreamEvent {
	events := d.closeTool(input.ToolID)
	payload := map[string]any{
		"toolId": input.ToolID,
		"result": buildToolResultValue(input),
	}
	events = append(events, NewEvent("tool.result", payload))
	return events
}

func (d *StreamEventDispatcher) handleActionArgs(input ActionArgs) []StreamEvent {
	events := d.closeForSwitch("action")
	if _, ok := d.state.openActions[input.ActionID]; !ok {
		d.state.openActions[input.ActionID] = actionBlockState{
			TaskID:      input.TaskID,
			Name:        input.ActionName,
			Description: input.Description,
		}
		events = append(events, NewEvent("action.start", map[string]any{
			"actionId":    input.ActionID,
			"runId":       d.request.RunID,
			"taskId":      input.TaskID,
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

func (d *StreamEventDispatcher) handlePlanUpdate(input PlanUpdate) []StreamEvent {
	eventType := "plan.update"
	d.state.planID = input.PlanID
	return []StreamEvent{NewEvent(eventType, map[string]any{
		"planId": input.PlanID,
		"plan":   input.Plan,
		"chatId": input.ChatID,
	})}
}

func (d *StreamEventDispatcher) handleTaskStart(input TaskStart) []StreamEvent {
	d.state.activeTaskID = input.TaskID
	return []StreamEvent{NewEvent("task.start", map[string]any{
		"taskId":      input.TaskID,
		"runId":       input.RunID,
		"taskName":    input.TaskName,
		"description": input.Description,
	})}
}

func (d *StreamEventDispatcher) handleTaskComplete(input TaskComplete) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.complete", map[string]any{
		"taskId": input.TaskID,
	})}
}

func (d *StreamEventDispatcher) handleTaskCancel(input TaskCancel) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.cancel", map[string]any{
		"taskId": input.TaskID,
	})}
}

func (d *StreamEventDispatcher) handleTaskFail(input TaskFail) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.fail", map[string]any{
		"taskId": input.TaskID,
		"error":  normalizeErrorMap(input.Error, "task_failed", "task", "runtime"),
	})}
}

func (d *StreamEventDispatcher) closeForSwitch(next string) []StreamEvent {
	switch next {
	case "reasoning":
		return append(d.closeContent(), append(d.closeAllTools(), d.closeAllActions()...)...)
	case "content":
		return append(d.closeReasoning(), append(d.closeAllTools(), d.closeAllActions()...)...)
	case "tool":
		return append(d.closeReasoning(), append(d.closeContent(), d.closeAllActions()...)...)
	case "action":
		return append(d.closeReasoning(), append(d.closeContent(), d.closeAllTools()...)...)
	default:
		return d.closeOpenBlocks()
	}
}

func (d *StreamEventDispatcher) closeOpenBlocks() []StreamEvent {
	events := d.closeReasoning()
	events = append(events, d.closeContent()...)
	events = append(events, d.closeAllTools()...)
	events = append(events, d.closeAllActions()...)
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

func (d *StreamEventDispatcher) closeAllTools() []StreamEvent {
	if len(d.state.openTools) == 0 {
		return nil
	}
	var events []StreamEvent
	for toolID := range d.state.openTools {
		events = append(events, d.closeTool(toolID)...)
	}
	return events
}

func (d *StreamEventDispatcher) closeTool(toolID string) []StreamEvent {
	block, ok := d.state.openTools[toolID]
	if !ok {
		return nil
	}
	delete(d.state.openTools, toolID)
	events := []StreamEvent{NewEvent("tool.end", map[string]any{
		"toolId": toolID,
	})}
	events = append(events, NewEvent("tool.snapshot", map[string]any{
		"toolId":          toolID,
		"runId":           d.request.RunID,
		"toolName":        block.Name,
		"taskId":          block.TaskID,
		"toolType":        block.Type,
		"toolLabel":       block.Label,
		"toolDescription": block.Description,
		"arguments":       d.state.toolArgsBuffer[toolID],
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

func normalizeErrorMap(input map[string]any, defaultCode string, defaultScope string, defaultCategory string) map[string]any {
	output := clonePayload(input)
	if output == nil {
		output = map[string]any{}
	}
	if _, ok := output["code"]; !ok {
		output["code"] = defaultCode
	}
	if _, ok := output["message"]; !ok {
		output["message"] = ""
	}
	if _, ok := output["scope"]; !ok {
		output["scope"] = defaultScope
	}
	if _, ok := output["category"]; !ok {
		output["category"] = defaultCategory
	}
	return output
}

func buildToolResultValue(input ToolResult) any {
	if input.Error == "" && input.ExitCode == 0 {
		return input.Result
	}
	result := map[string]any{
		"output": input.Result,
	}
	if input.ExitCode != 0 {
		result["exitCode"] = input.ExitCode
	}
	if input.Error != "" {
		result["error"] = input.Error
	}
	return result
}
