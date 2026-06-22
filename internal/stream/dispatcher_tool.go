package stream

import "strings"

func (d *StreamEventDispatcher) handleToolArgs(input ToolArgs) []StreamEvent {
	events := d.closeForSwitch("tool")
	taskID := input.TaskID
	if strings.TrimSpace(taskID) == "" && d.state.activeTaskID != "" {
		taskID = d.state.activeTaskID
	}
	if _, ok := d.state.openTools[input.ToolID]; !ok {
		d.state.openTools[input.ToolID] = toolBlockState{
			TaskID:      taskID,
			Name:        input.ToolName,
			Label:       input.ToolLabel,
			Description: input.ToolDescription,
		}
		events = append(events, NewEvent("tool.start", map[string]any{
			"toolId":          input.ToolID,
			"runId":           d.request.RunID,
			"taskId":          taskID,
			"toolName":        input.ToolName,
			"toolLabel":       input.ToolLabel,
			"toolDescription": input.ToolDescription,
		}))
		if input.AwaitAsk != nil {
			if event := d.newAwaitAskEvent(*input.AwaitAsk); event.Type != "" {
				events = append(events, event)
			}
		}
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
	return d.closeTool(input.ToolID, input.FileChange)
}

func (d *StreamEventDispatcher) handleToolResult(input ToolResult) []StreamEvent {
	events := d.closeTool(input.ToolID, nil)
	payload := map[string]any{
		"toolId":   input.ToolID,
		"toolName": input.ToolName,
		"result":   buildToolResultValue(input),
	}
	if len(input.FileChange) > 0 {
		payload["fileChange"] = clonePayload(input.FileChange)
	}
	if len(input.Hitl) > 0 {
		payload["approval"] = clonePayload(input.Hitl)
	}
	resultEvent := NewEvent("tool.result", payload)
	if startedAt, ok := d.state.toolEndAtByID[input.ToolID]; ok {
		resultEvent.Payload["durationMs"] = nonNegativeDurationMs(startedAt, resultEvent.Timestamp)
		delete(d.state.toolEndAtByID, input.ToolID)
	}
	events = append(events, resultEvent)
	if eventType, memoryPayload := d.memoryToolResultEvent(input); eventType != "" && len(memoryPayload) > 0 {
		events = append(events, NewEvent(eventType, memoryPayload))
	}
	return events
}

func (d *StreamEventDispatcher) memoryToolResultEvent(input ToolResult) (string, map[string]any) {
	eventType := memoryToolEventType(input.ToolName)
	if eventType == "" {
		return "", nil
	}
	data := map[string]any{
		"toolId":   input.ToolID,
		"toolName": input.ToolName,
		"result":   buildToolResultValue(input),
	}
	if len(input.Hitl) > 0 {
		data["approval"] = clonePayload(input.Hitl)
	}
	return eventType, map[string]any{
		"runId":  d.request.RunID,
		"chatId": d.request.ChatID,
		"data":   data,
	}
}

func memoryToolEventType(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case "memory_write":
		return "memory.write"
	case "memory_read":
		return "memory.read"
	case "memory_search":
		return "memory.search"
	case "memory_update":
		return "memory.update"
	case "memory_forget":
		return "memory.forget"
	case "memory_timeline":
		return "memory.timeline"
	case "memory_promote":
		return "memory.promote"
	case "memory_consolidate":
		return "memory.consolidate"
	default:
		return ""
	}
}

func (d *StreamEventDispatcher) closeAllTools() []StreamEvent {
	if len(d.state.openTools) == 0 {
		return nil
	}
	var events []StreamEvent
	for toolID := range d.state.openTools {
		events = append(events, d.closeTool(toolID, nil)...)
	}
	return events
}

func (d *StreamEventDispatcher) closeTool(toolID string, fileChange map[string]any) []StreamEvent {
	block, ok := d.state.openTools[toolID]
	if !ok {
		return nil
	}
	delete(d.state.openTools, toolID)
	endPayload := map[string]any{
		"toolId": toolID,
	}
	if len(fileChange) > 0 {
		endPayload["fileChange"] = clonePayload(fileChange)
	}
	endEvent := NewEvent("tool.end", endPayload)
	d.state.toolEndAtByID[toolID] = endEvent.Timestamp
	events := []StreamEvent{endEvent}
	snapshotPayload := map[string]any{
		"toolId":          toolID,
		"runId":           d.request.RunID,
		"toolName":        block.Name,
		"taskId":          block.TaskID,
		"toolLabel":       block.Label,
		"toolDescription": block.Description,
		"arguments":       d.state.toolArgsBuffer[toolID],
	}
	if len(fileChange) > 0 {
		snapshotPayload["fileChange"] = clonePayload(fileChange)
	}
	events = append(events, NewEvent("tool.snapshot", snapshotPayload))
	return events
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
