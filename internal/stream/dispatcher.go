package stream

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"sort"
	"strings"
	"time"
)

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
		artifactCount := value.ArtifactCount
		if artifactCount <= 0 {
			artifactCount = len(value.Artifacts)
		}
		artifacts := append([]map[string]any(nil), value.Artifacts...)
		return []StreamEvent{NewEvent("artifact.publish", map[string]any{
			"chatId":        value.ChatID,
			"runId":         value.RunID,
			"artifactCount": artifactCount,
			"artifacts":     artifacts,
		})}
	case SourcePublish:
		return d.handleSourcePublish(value)
	case AwaitAsk:
		event := d.newAwaitAskEvent(value)
		if event.Type == "" {
			return nil
		}
		return []StreamEvent{event}
	case RequestSubmit:
		return []StreamEvent{NewEvent("request.submit", map[string]any{
			"requestId":  value.RequestID,
			"chatId":     value.ChatID,
			"runId":      value.RunID,
			"awaitingId": value.AwaitingID,
			"params":     value.Params,
		})}
	case AwaitingAnswer:
		event := newAwaitingAnswerEvent(value)
		if event.Type == "" {
			return nil
		}
		return []StreamEvent{event}
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
		payload := map[string]any{"runId": value.RunID}
		if usage := d.usagePayload(); usage != nil {
			payload["usage"] = usage
		}
		events = append(events, NewEvent("run.cancel", payload))
		d.state.terminated = true
		return events
	case InputDebugPreCall:
		return []StreamEvent{NewEvent("debug.preCall", map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
			"data": map[string]any{
				"provider": map[string]any{
					"key":      value.ProviderKey,
					"endpoint": value.ProviderEndpoint,
				},
				"model": map[string]any{
					"key": value.ModelKey,
					"id":  value.ModelID,
				},
				"requestBody": clonePayload(value.RequestBody),
				"contextWindow": map[string]any{
					"max_size":       value.ContextWindow,
					"actual_size":    value.CurrentContextSize,
					"estimated_size": value.EstimatedNextCallSize,
				},
				"usage": map[string]any{
					"runUsage": map[string]any{
						"promptTokens":     value.RunPromptTokens,
						"completionTokens": value.RunCompletionTokens,
						"totalTokens":      value.RunTotalTokens,
					},
				},
			},
		})}
	case InputDebugPostCall:
		if value.RunTotalTokens > 0 {
			d.state.runUsage = &runUsageState{
				PromptTokens:     value.RunPromptTokens,
				CompletionTokens: value.RunCompletionTokens,
				TotalTokens:      value.RunTotalTokens,
			}
		}
		return []StreamEvent{NewEvent("debug.postCall", map[string]any{
			"runId":  d.request.RunID,
			"chatId": value.ChatID,
			"data": map[string]any{
				"model": map[string]any{
					"key": value.ModelKey,
				},
				"contextWindow": map[string]any{
					"max_size":       value.ContextWindow,
					"actual_size":    value.CurrentContextSize,
					"estimated_size": value.EstimatedNextCallSize,
				},
				"usage": map[string]any{
					"llmReturnUsage": map[string]any{
						"promptTokens":     value.LLMReturnPromptTokens,
						"completionTokens": value.LLMReturnCompletionTokens,
						"totalTokens":      value.LLMReturnTotalTokens,
					},
					"runUsage": map[string]any{
						"promptTokens":     value.RunPromptTokens,
						"completionTokens": value.RunCompletionTokens,
						"totalTokens":      value.RunTotalTokens,
					},
				},
			},
		})}
	case InputRunComplete:
		d.state.runFinishReason = value.FinishReason
		return nil
	case InputRunError:
		events := d.closeOpenBlocks()
		payload := map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(value.Error, "run_error", "run", "runtime"),
		}
		if usage := d.usagePayload(); usage != nil {
			payload["usage"] = usage
		}
		events = append(events, NewEvent("run.error", payload))
		d.state.terminated = true
		return events
	default:
		return nil
	}
}

func newAwaitingAnswerEvent(input AwaitingAnswer) StreamEvent {
	answer := clonePayload(input.Answer)
	if len(answer) == 0 {
		return StreamEvent{}
	}
	mode := strings.ToLower(strings.TrimSpace(anyString(answer["mode"])))
	if mode == "" {
		return StreamEvent{}
	}
	payload := map[string]any{
		"awaitingId": input.AwaitingID,
		"mode":       mode,
	}
	status := strings.ToLower(strings.TrimSpace(anyString(answer["status"])))
	if status == "" {
		return StreamEvent{}
	}
	payload["status"] = status
	if status == "error" {
		if errPayload := anyMap(answer["error"]); len(errPayload) > 0 {
			entry := map[string]any{}
			if code := strings.TrimSpace(anyString(errPayload["code"])); code != "" {
				entry["code"] = code
			}
			if message := strings.TrimSpace(anyString(errPayload["message"])); message != "" {
				entry["message"] = message
			}
			if len(entry) > 0 {
				payload["error"] = entry
			}
		}
		return NewEvent("awaiting.answer", payload)
	}
	if status != "answered" {
		return StreamEvent{}
	}
	switch mode {
	case "question":
		formatted := formatAwaitingAnswers(answer["answers"])
		if len(formatted) > 0 {
			payload["answers"] = formatted
		}
	case "approval":
		formatted := formatAwaitingApprovals(answer["approvals"])
		if len(formatted) > 0 {
			payload["approvals"] = formatted
		}
	case "form":
		formatted := formatAwaitingForms(answer["forms"])
		if len(formatted) > 0 {
			payload["forms"] = formatted
		}
	default:
		return StreamEvent{}
	}
	return NewEvent("awaiting.answer", payload)
}

func formatAwaitingAnswers(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return formatAwaitingAnswers(items)
	case []any:
		formatted := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			answer, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(anyString(answer["id"]))
			if id == "" {
				continue
			}
			entry := map[string]any{
				"id": id,
			}
			if question := strings.TrimSpace(anyString(answer["question"])); question != "" {
				entry["question"] = question
			}
			if header := strings.TrimSpace(anyString(answer["header"])); header != "" {
				entry["header"] = header
			}
			appendAwaitingQuestionValue(entry, answer["answer"])
			if value := strings.TrimSpace(anyString(answer["value"])); value != "" {
				entry["value"] = value
			}
			formatted = append(formatted, entry)
		}
		return formatted
	default:
		return nil
	}
}

func formatAwaitingApprovals(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return formatAwaitingApprovals(items)
	case []any:
		formatted := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			approval := anyMap(item)
			id := strings.TrimSpace(anyString(approval["id"]))
			decision := strings.ToLower(strings.TrimSpace(anyString(approval["decision"])))
			if id == "" || decision == "" {
				continue
			}
			entry := map[string]any{
				"id":       id,
				"decision": decision,
			}
			if command := strings.TrimSpace(anyString(approval["command"])); command != "" {
				entry["command"] = command
			}
			if reason := strings.TrimSpace(anyString(approval["reason"])); reason != "" {
				entry["reason"] = reason
			}
			formatted = append(formatted, entry)
		}
		return formatted
	default:
		return nil
	}
}

func formatAwaitingForms(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return formatAwaitingForms(items)
	case []any:
		formatted := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			form := anyMap(item)
			id := strings.TrimSpace(anyString(form["id"]))
			action := strings.ToLower(strings.TrimSpace(anyString(form["action"])))
			if id == "" || action == "" {
				continue
			}
			entry := map[string]any{
				"id":     id,
				"action": action,
			}
			if payload, ok := form["payload"].(map[string]any); ok && len(payload) > 0 {
				entry["payload"] = clonePayload(payload)
			}
			if reason := strings.TrimSpace(anyString(form["reason"])); reason != "" {
				entry["reason"] = reason
			}
			if command := strings.TrimSpace(anyString(form["command"])); command != "" {
				entry["command"] = command
			}
			formatted = append(formatted, entry)
		}
		return formatted
	default:
		return nil
	}
}

func appendAwaitingQuestionValue(entry map[string]any, value any) {
	switch typed := value.(type) {
	case []string:
		entry["answers"] = append([]string(nil), typed...)
	case []any:
		entry["answers"] = append([]any(nil), typed...)
	default:
		entry["answer"] = value
	}
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func anyMap(value any) map[string]any {
	item, _ := value.(map[string]any)
	return item
}

func (d *StreamEventDispatcher) Complete() []StreamEvent {
	if d.state.terminated {
		return nil
	}
	events := d.closeOpenBlocks()
	if d.state.runError != nil {
		payload := map[string]any{
			"runId": d.request.RunID,
			"error": normalizeErrorMap(d.state.runError, "stream_failed", "run", "runtime"),
		}
		if usage := d.usagePayload(); usage != nil {
			payload["usage"] = usage
		}
		events = append(events, NewEvent("run.error", payload))
		d.state.terminated = true
		return events
	}
	completePayload := map[string]any{
		"runId":        d.request.RunID,
		"finishReason": d.state.runFinishReason,
	}
	if usage := d.usagePayload(); usage != nil {
		completePayload["usage"] = usage
	}
	events = append(events, NewEvent("run.complete", completePayload))
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
	payload := map[string]any{
		"runId": d.request.RunID,
		"error": normalizeErrorMap(d.state.runError, "stream_failed", "run", "runtime"),
	}
	if usage := d.usagePayload(); usage != nil {
		payload["usage"] = usage
	}
	events = append(events, NewEvent("run.error", payload))
	d.state.terminated = true
	return events
}

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

func (d *StreamEventDispatcher) newAwaitAskEvent(input AwaitAsk) StreamEvent {
	awaitingID := strings.TrimSpace(input.AwaitingID)
	if awaitingID == "" {
		return StreamEvent{}
	}
	if d.state.emittedAwaitings[awaitingID] {
		log.Printf("[stream][run:%s][warn] duplicate awaiting.ask ignored awaitingId=%s", d.request.RunID, awaitingID)
		return StreamEvent{}
	}
	d.state.emittedAwaitings[awaitingID] = true
	payload := map[string]any{
		"awaitingId": awaitingID,
		"mode":       input.Mode,
		"timeout":    input.Timeout,
		"runId":      input.RunID,
	}
	if strings.EqualFold(strings.TrimSpace(input.Mode), "form") {
		if strings.TrimSpace(input.ViewportType) != "" {
			payload["viewportType"] = input.ViewportType
		}
		if strings.TrimSpace(input.ViewportKey) != "" {
			payload["viewportKey"] = input.ViewportKey
		}
	}
	if len(input.Questions) > 0 {
		payload["questions"] = input.Questions
	}
	if len(input.Approvals) > 0 {
		payload["approvals"] = input.Approvals
	}
	if len(input.Forms) > 0 {
		payload["forms"] = input.Forms
	}
	return NewEvent("awaiting.ask", payload)
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
	if len(input.Hitl) > 0 {
		payload["approval"] = clonePayload(input.Hitl)
	}
	events = append(events, NewEvent("tool.result", payload))
	return events
}

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
	payload := map[string]any{
		"taskId":      input.TaskID,
		"runId":       input.RunID,
		"groupId":     input.GroupID,
		"taskName":    input.TaskName,
		"description": input.Description,
		"subAgentKey": input.SubAgentKey,
		"mainToolId":  input.MainToolID,
	}
	return []StreamEvent{NewEvent("task.start", payload)}
}

func (d *StreamEventDispatcher) handleTaskComplete(input TaskComplete) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.complete", map[string]any{
		"taskId": input.TaskID,
		"status": input.Status,
	})}
}

func (d *StreamEventDispatcher) handleTaskCancel(input TaskCancel) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.cancel", map[string]any{
		"taskId": input.TaskID,
		"status": input.Status,
	})}
}

func (d *StreamEventDispatcher) handleTaskFail(input TaskFail) []StreamEvent {
	if d.state.activeTaskID == input.TaskID {
		d.state.activeTaskID = ""
	}
	return []StreamEvent{NewEvent("task.fail", map[string]any{
		"taskId": input.TaskID,
		"status": input.Status,
		"error":  normalizeErrorMap(input.Error, "task_failed", "task", "runtime"),
	})}
}

func (d *StreamEventDispatcher) handleSourcePublish(input SourcePublish) []StreamEvent {
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = d.request.RunID
	}

	sources, chunkCount := normalizeSources(input.Sources)
	payload := map[string]any{
		"publishId":   sourcePublishID(input.PublishID),
		"runId":       runID,
		"kind":        input.Kind,
		"sourceCount": len(sources),
		"chunkCount":  chunkCount,
		"sources":     sources,
	}
	if taskID := strings.TrimSpace(input.TaskID); taskID != "" {
		payload["taskId"] = taskID
	}
	if toolID := strings.TrimSpace(input.ToolID); toolID != "" {
		payload["toolId"] = toolID
	}
	if query := strings.TrimSpace(input.Query); query != "" {
		payload["query"] = query
	}

	return []StreamEvent{NewEvent("source.publish", payload)}
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

func (d *StreamEventDispatcher) usagePayload() map[string]any {
	if d.state.runUsage == nil || d.state.runUsage.TotalTokens == 0 {
		return nil
	}
	return map[string]any{
		"promptTokens":     d.state.runUsage.PromptTokens,
		"completionTokens": d.state.runUsage.CompletionTokens,
		"totalTokens":      d.state.runUsage.TotalTokens,
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

func normalizeSources(input []Source) ([]Source, int) {
	if len(input) == 0 {
		return []Source{}, 0
	}

	sources := make([]Source, 0, len(input))
	chunkCount := 0
	for _, source := range input {
		chunks := make([]SourceChunk, len(source.Chunks))
		copy(chunks, source.Chunks)
		sort.SliceStable(chunks, func(i, j int) bool {
			return chunks[i].Index < chunks[j].Index
		})

		chunkIndexes := make([]int, 0, len(chunks))
		minIndex := 0
		if len(chunks) > 0 {
			minIndex = chunks[0].Index
		}
		for _, chunk := range chunks {
			chunkIndexes = append(chunkIndexes, chunk.Index)
		}

		sources = append(sources, Source{
			ID:             source.ID,
			Name:           source.Name,
			Title:          source.Title,
			Icon:           source.Icon,
			URL:            source.URL,
			Link:           source.Link,
			CollectionID:   source.CollectionID,
			CollectionName: source.CollectionName,
			ChunkIndexes:   chunkIndexes,
			MinIndex:       minIndex,
			Chunks:         chunks,
		})
		chunkCount += len(chunks)
	}

	return sources, chunkCount
}

func sourcePublishID(input string) string {
	if id := strings.TrimSpace(input); id != "" {
		return id
	}
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "src-" + hex.EncodeToString(buf)
	}
	return "src-" + strconvBase16(time.Now().UnixNano())
}

func strconvBase16(value int64) string {
	const digits = "0123456789abcdef"
	if value == 0 {
		return "0"
	}
	if value < 0 {
		value = -value
	}
	out := make([]byte, 0, 16)
	for value > 0 {
		out = append(out, digits[value%16])
		value /= 16
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
