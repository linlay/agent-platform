package stream

import (
	"fmt"
	"strings"
)

func (d *StreamEventDispatcher) handleModelTurnDiscard(input ModelTurnDiscard) []StreamEvent {
	if d == nil || d.state == nil {
		return nil
	}
	return d.discardModelTurn(input, d.resolveTaskID(input.TaskID))
}

func (d *StreamEventDispatcher) discardModelTurn(input ModelTurnDiscard, taskID string) []StreamEvent {
	scope := taskScope(taskID)

	reasoningIDs := compactIDs(input.ReasoningIDs)
	contentIDs := compactIDs(input.ContentIDs)
	toolIDs := compactIDs(input.ToolIDs)

	if active, ok := d.state.activeReasonings[scope]; ok {
		reasoningIDs = appendIDIfMissing(reasoningIDs, active.ID)
		delete(d.state.activeReasonings, scope)
	}
	if active, ok := d.state.activeContents[scope]; ok {
		contentIDs = appendIDIfMissing(contentIDs, active.ID)
		delete(d.state.activeContents, scope)
	}
	for toolID, block := range d.state.openTools {
		if taskScope(block.TaskID) != scope {
			continue
		}
		toolIDs = appendIDIfMissing(toolIDs, toolID)
		delete(d.state.openTools, toolID)
	}
	var events []StreamEvent
	for _, group := range []struct {
		kind string
		ids  []string
	}{{"reasoning", reasoningIDs}, {"content", contentIDs}, {"tool", toolIDs}} {
		for _, id := range group.ids {
			if event, ok := d.failModelBlock(group.kind, id, taskID, input.Error, input.Reason); ok {
				events = append(events, event)
			}
		}
	}
	for _, id := range reasoningIDs {
		delete(d.state.reasoningBuffer, id)
	}
	for _, id := range contentIDs {
		delete(d.state.contentBuffer, id)
		delete(d.state.contentGuards, id)
	}
	for _, id := range toolIDs {
		delete(d.state.toolArgsBuffer, id)
		delete(d.state.toolEndAtByID, id)
		delete(d.state.emittedAwaitings, id)
	}

	status := "discarded"
	message := "已丢弃未完成的模型响应"
	if input.Retrying {
		status = "retrying"
		message = "模型响应不完整，已丢弃并正在重试"
		if input.RetryDelayMs > 0 {
			message = fmt.Sprintf("正在重试（第 %d/%d 次），等待 %g 秒后发起请求", input.Attempt-1, input.MaxAttempts-1, float64(input.RetryDelayMs)/1000)
		}
	}
	payload := map[string]any{
		"runId":   d.request.RunID,
		"chatId":  d.request.ChatID,
		"phase":   "model_call",
		"runSeq":  input.RunSeq,
		"status":  status,
		"message": message,
	}
	if taskID != "" {
		payload["taskId"] = taskID
	}
	if input.Retrying {
		retry := map[string]any{
			"attempt":     input.Attempt,
			"maxAttempts": input.MaxAttempts,
		}
		if input.RetryDelayMs > 0 {
			retry["delayMs"] = input.RetryDelayMs
			retry["retryAt"] = input.RetryAt
		}
		if reason := strings.TrimSpace(input.Reason); reason != "" {
			retry["reason"] = reason
		}
		if input.TimeoutSeconds > 0 {
			retry["timeoutSeconds"] = input.TimeoutSeconds
		}
		if input.ElapsedMs > 0 {
			retry["elapsedMs"] = input.ElapsedMs
		}
		payload["retry"] = retry
	}
	return append(events, NewEvent("run.activity", payload))
}

func compactIDs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = appendIDIfMissing(out, value)
	}
	return out
}

func appendIDIfMissing(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// Failed attempt output is a display-only terminal event, never a model message.
// A normal end before commit closes a segment, not the attempt: failure may
// subsequently mark that segment failed, preserving its text for cold replay.
func (d *StreamEventDispatcher) failModelBlock(kind, id, taskID string, cause map[string]any, reason string) (StreamEvent, bool) {
	key := kind + ":" + id
	start, ok := d.state.blockStarts[key]
	if !ok {
		return StreamEvent{}, false
	}
	delete(d.state.blockStarts, key)
	payload := clonePayload(start.Payload)
	payload["startedAt"] = start.Timestamp
	payload["status"] = "failed"
	if reason == "" {
		reason = "provider_stream_failed"
	}
	payload["error"] = normalizeErrorMap(cause, reason, "model", "model")
	if taskID != "" {
		payload["taskId"] = taskID
	}
	switch kind {
	case "reasoning":
		payload["text"] = d.state.reasoningBuffer[id]
	case "content":
		payload["text"] = d.state.contentBuffer[id]
	case "tool":
		payload["arguments"] = d.state.toolArgsBuffer[id]
		delete(d.state.endedTools, id)
	}
	return NewEvent(kind+".end", payload), true
}

// Fail only open segments for terminal errors; committed/executed tools are not
// open parameter streams and must retain their actual tool.result.
func (d *StreamEventDispatcher) failOpenBlocks(cause map[string]any) []StreamEvent {
	scopes := map[string]bool{}
	for scope := range d.state.activeReasonings {
		scopes[scope] = true
	}
	for scope := range d.state.activeContents {
		scopes[scope] = true
	}
	for _, block := range d.state.openTools {
		scopes[taskScope(block.TaskID)] = true
	}
	var events []StreamEvent
	for scope := range scopes {
		for _, event := range d.discardModelTurn(ModelTurnDiscard{TaskID: scope, Error: cause}, scope) {
			if event.Type != "run.activity" {
				events = append(events, event)
			}
		}
	}
	return events
}
