package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func stepLineStartsModelCall(line StepLine) bool {
	if usageHasLLMChat(line.Usage) {
		return true
	}
	return storedMessagesContainAssistant(line.Messages)
}

func usageHasLLMChat(usage map[string]any) bool {
	if len(usage) == 0 {
		return false
	}
	return toIntFromKeys(usage, "llmChatCompletionCount", "llm_chat_completion_count") > 0 ||
		toIntFromKeys(usage, "toolCallCount", "tool_call_count") > 0
}

func storedMessagesContainAssistant(messages []StoredMessage) bool {
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			return true
		}
	}
	return false
}

func stepLineCanReuseReactSeq(line StepLine) bool {
	return len(line.Messages) > 0 || len(line.Awaiting) > 0
}

func stepLineIsReactToolContinuation(line StepLine) bool {
	if storedMessagesContainAssistant(line.Messages) {
		return false
	}
	for _, message := range line.Messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			return true
		}
	}
	return false
}

func approvalMatchesToolMessages(approval *StepApproval, messages []StoredMessage) bool {
	if approval == nil {
		return false
	}
	toolIDs := map[string]struct{}{}
	hasToolMessage := false
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		hasToolMessage = true
		for _, id := range []string{message.ToolCallID, message.ToolID, message.ActionID} {
			id = strings.TrimSpace(id)
			if id != "" {
				toolIDs[id] = struct{}{}
			}
		}
	}
	if !hasToolMessage {
		return false
	}
	sawDecisionID := false
	for _, decision := range approval.Decisions {
		toolID := strings.TrimSpace(decision.ToolID)
		if toolID == "" {
			continue
		}
		sawDecisionID = true
		if _, ok := toolIDs[toolID]; ok {
			return true
		}
	}
	return !sawDecisionID
}

func approvalAuditMessage(approval *StepApproval, ts int64) (StoredMessage, bool) {
	if approval == nil {
		return StoredMessage{}, false
	}
	notice := approval.Notice
	if strings.TrimSpace(notice) == "" {
		notice = approval.Summary
	}
	if strings.TrimSpace(notice) == "" {
		return StoredMessage{}, false
	}
	messageTS := ts
	return StoredMessage{
		Role:     "user",
		Content:  textContent(notice),
		Approval: cloneStepApproval(approval),
		Ts:       &messageTS,
	}, true
}

func cloneStepApproval(approval *StepApproval) *StepApproval {
	if approval == nil {
		return nil
	}
	cloned := *approval
	if len(approval.Decisions) > 0 {
		cloned.Decisions = append([]StepApprovalDecision(nil), approval.Decisions...)
		for index := range cloned.Decisions {
			if len(cloned.Decisions[index].Payload) > 0 {
				cloned.Decisions[index].Payload = cloneStringAnyMap(cloned.Decisions[index].Payload)
			}
		}
	}
	return &cloned
}

func (w *StepWriter) ensureMsgID() {
	if w.currentMsgID == "" || w.needNewMsgID {
		w.currentMsgID = generateMsgID()
		w.needNewMsgID = false
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func textContent(text string) []ContentPart {
	return []ContentPart{{Type: "text", Text: text}}
}

func formatResult(v any) string {
	if v == nil {
		return ""
	}
	if text, ok := v.(string); ok {
		return text
	}
	if data, err := json.Marshal(v); err == nil {
		return string(data)
	}
	return fmt.Sprintf("%v", v)
}

func upsertStoredMessage(messages []StoredMessage, message StoredMessage) []StoredMessage {
	key := storedMessageUpsertKey(message)
	if key == "" {
		return append(messages, message)
	}
	for index := range messages {
		if storedMessageUpsertKey(messages[index]) == key {
			messages[index] = mergeStoredMessageSnapshot(messages[index], message)
			return messages
		}
	}
	return append(messages, message)
}

func storedMessageUpsertKey(message StoredMessage) string {
	if id := strings.TrimSpace(message.ContentID); id != "" {
		return "content:" + id
	}
	if id := strings.TrimSpace(message.ReasoningID); id != "" {
		return "reasoning:" + id
	}
	if id := strings.TrimSpace(message.ToolID); id != "" {
		return strings.TrimSpace(message.Role) + ":tool:" + id
	}
	if id := strings.TrimSpace(message.ActionID); id != "" {
		return strings.TrimSpace(message.Role) + ":action:" + id
	}
	if id := strings.TrimSpace(message.ToolCallID); id != "" {
		return strings.TrimSpace(message.Role) + ":tool-call:" + id
	}
	return ""
}

func mergeStoredMessageSnapshot(existing StoredMessage, incoming StoredMessage) StoredMessage {
	if storedMessageTextLen(incoming) < storedMessageTextLen(existing) {
		return existing
	}
	return incoming
}

func storedMessageTextLen(message StoredMessage) int {
	total := 0
	for _, part := range message.Content {
		total += len(part.Text)
	}
	for _, part := range message.ReasoningContent {
		total += len(part.Text)
	}
	for _, call := range message.ToolCalls {
		total += len(call.Function.Arguments)
	}
	return total
}

func stringVal(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// toMapSlice converts an any value to []map[string]any.
// Handles both []any (from JSON unmarshal) and []map[string]any (from Go engine).

func toMapSlice(v any) []map[string]any {
	switch typed := v.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
		return result
	default:
		return nil
	}
}

func generateMsgID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "m_" + hex.EncodeToString(b)
}

func cloneStepSystemPayload(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return cloneStringAnyMap(value)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return cloneStringAnyMap(value)
	}
	return cloned
}

func buildContextWindow(usage map[string]any, maxSize int, estimatedSize int) map[string]any {
	actual := 0
	if usage != nil {
		actual = toIntFromKeys(usage, "promptTokens", "prompt_tokens")
	}
	cw := map[string]any{}
	if maxSize > 0 {
		cw["maxSize"] = maxSize
	}
	if actual > 0 {
		cw["actualSize"] = actual
	}
	if estimatedSize > 0 {
		cw["estimatedSize"] = estimatedSize
	}
	if modelKey := firstStringFromKeys(usage, "modelKey", "model_key"); modelKey != "" {
		cw["modelKey"] = modelKey
	}
	if reasoningEffort := firstStringFromKeys(usage, "reasoningEffort", "reasoning_effort"); reasoningEffort != "" {
		cw["reasoningEffort"] = reasoningEffort
	}
	if len(cw) == 0 {
		return nil
	}
	return cw
}

func captureStepModelMetadata(modelKey *string, reasoningEffort *string, values ...map[string]any) {
	if modelKey != nil && strings.TrimSpace(*modelKey) == "" {
		for _, value := range values {
			if text := firstStringFromKeys(value, "modelKey", "model_key"); text != "" {
				*modelKey = text
				break
			}
		}
	}
	if reasoningEffort != nil && strings.TrimSpace(*reasoningEffort) == "" {
		for _, value := range values {
			if text := firstStringFromKeys(value, "reasoningEffort", "reasoning_effort"); text != "" {
				*reasoningEffort = text
				break
			}
		}
	}
}

func applyStepLineModelMetadata(line *StepLine, modelKey string, reasoningEffort string) {
	if line == nil {
		return
	}
	line.ModelKey = firstNonEmptyStepString(
		line.ModelKey,
		modelKey,
		firstStringFromKeys(line.Usage, "modelKey", "model_key"),
		firstStringFromKeys(line.ContextWindow, "modelKey", "model_key"),
	)
	line.ReasoningEffort = firstNonEmptyStepString(
		line.ReasoningEffort,
		reasoningEffort,
		firstStringFromKeys(line.Usage, "reasoningEffort", "reasoning_effort"),
		firstStringFromKeys(line.ContextWindow, "reasoningEffort", "reasoning_effort"),
	)
	stripStepModelMetadata(line.Usage)
	stripStepModelMetadata(line.ContextWindow)
}

func stripStepModelMetadata(values map[string]any) {
	if values == nil {
		return
	}
	delete(values, "modelKey")
	delete(values, "model_key")
	delete(values, "reasoningEffort")
	delete(values, "reasoning_effort")
}

func firstNonEmptyStepString(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func systemRefFromDebugData(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	raw, _ := value["systemRef"].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	return cloneStepSystemPayload(raw)
}

// parseStage normalises a stage marker string to a stage name, matching Java's
// TurnTraceWriter.parseStage behaviour.

func parseStage(marker string) string {
	marker = strings.TrimSpace(marker)
	switch {
	case strings.HasPrefix(marker, "react-step"):
		return "react"
	case marker == "plan-draft" || marker == "plan-generate":
		return "plan"
	case strings.HasPrefix(marker, "execute-task"):
		return "execute"
	case marker == "summary":
		return "summary"
	default:
		return marker
	}
}
