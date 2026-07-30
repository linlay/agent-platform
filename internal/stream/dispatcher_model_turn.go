package stream

import "strings"

const discardIncompleteModelTurnAction = "discard_incomplete_model_turn"

func (d *StreamEventDispatcher) handleModelTurnDiscard(input ModelTurnDiscard) []StreamEvent {
	if d == nil || d.state == nil {
		return nil
	}
	taskID := d.resolveTaskID(input.TaskID)
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
	for _, id := range reasoningIDs {
		delete(d.state.reasoningBuffer, id)
	}
	for _, id := range contentIDs {
		delete(d.state.contentBuffer, id)
	}
	for _, id := range toolIDs {
		delete(d.state.toolArgsBuffer, id)
		delete(d.state.toolEndAtByID, id)
		delete(d.state.emittedAwaitings, id)
	}
	recovery := map[string]any{
		"action": discardIncompleteModelTurnAction,
		"runSeq": input.RunSeq,
	}
	if len(reasoningIDs) > 0 {
		recovery["reasoningIds"] = reasoningIDs
	}
	if len(contentIDs) > 0 {
		recovery["contentIds"] = contentIDs
	}
	if len(toolIDs) > 0 {
		recovery["toolIds"] = toolIDs
	}

	status := "discarded"
	message := "已丢弃未完成的模型响应"
	if input.Retrying {
		status = "retrying"
		message = "模型响应不完整，已丢弃并正在重试"
	}
	payload := map[string]any{
		"runId":    d.request.RunID,
		"chatId":   d.request.ChatID,
		"phase":    "model_call",
		"status":   status,
		"message":  message,
		"recovery": recovery,
	}
	if taskID != "" {
		payload["taskId"] = taskID
	}
	if input.Retrying {
		retry := map[string]any{
			"attempt":     input.Attempt,
			"maxAttempts": input.MaxAttempts,
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
	return []StreamEvent{NewEvent("run.activity", payload)}
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
