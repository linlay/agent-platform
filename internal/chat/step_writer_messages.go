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
	return toIntFromKeys(usage, "llmChatCompletionCount") > 0 ||
		toIntFromKeys(usage, "toolCallCount") > 0
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

func durationMsPointer(value any, payload map[string]any) *int64 {
	if payload == nil {
		return nil
	}
	if _, ok := payload["durationMs"]; !ok {
		return nil
	}
	duration := int64FromAny(value)
	if duration < 0 {
		duration = 0
	}
	return &duration
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
	if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") && len(message.ToolCalls) > 0 {
		if id := strings.TrimSpace(message.MsgID); id != "" {
			return "assistant:tool-calls:" + id
		}
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
	if strings.EqualFold(strings.TrimSpace(existing.Role), "assistant") &&
		strings.EqualFold(strings.TrimSpace(incoming.Role), "assistant") &&
		len(existing.ToolCalls) > 0 && len(incoming.ToolCalls) > 0 {
		return mergeAssistantToolCallMessage(existing, incoming)
	}
	if storedMessageTextLen(incoming) < storedMessageTextLen(existing) {
		return existing
	}
	return incoming
}

func mergeAssistantToolCallMessage(existing StoredMessage, incoming StoredMessage) StoredMessage {
	merged := existing
	if merged.MsgID == "" {
		merged.MsgID = incoming.MsgID
	}
	if merged.Ts == nil {
		merged.Ts = incoming.Ts
	}
	if len(merged.ToolCalls) == 0 {
		merged.ToolCalls = append([]StoredToolCall(nil), incoming.ToolCalls...)
		return merged
	}
	for _, incomingCall := range incoming.ToolCalls {
		key := storedToolCallKey(incomingCall)
		replaced := false
		for index, existingCall := range merged.ToolCalls {
			if key == "" || storedToolCallKey(existingCall) != key {
				continue
			}
			if storedToolCallTextLen(incomingCall) >= storedToolCallTextLen(existingCall) {
				merged.ToolCalls[index] = incomingCall
			}
			replaced = true
			break
		}
		if !replaced {
			merged.ToolCalls = append(merged.ToolCalls, incomingCall)
		}
	}
	return merged
}

func storedToolCallKey(call StoredToolCall) string {
	if id := strings.TrimSpace(call.ToolID); id != "" {
		return "tool:" + id
	}
	if id := strings.TrimSpace(call.ActionID); id != "" {
		return "action:" + id
	}
	if id := strings.TrimSpace(call.ID); id != "" {
		return "call:" + id
	}
	return ""
}

func storedToolCallTextLen(call StoredToolCall) int {
	return len(call.Function.Arguments) + len(call.Function.Name) + len(call.ID) + len(call.ToolID) + len(call.ActionID)
}

func canonicalizeStoredToolResultOrder(messages []StoredMessage) []StoredMessage {
	if len(messages) < 2 {
		return messages
	}
	toolOrder := make([]string, 0)
	seenToolCalls := map[string]struct{}{}
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		for _, toolCall := range message.ToolCalls {
			id := strings.TrimSpace(toolCall.ID)
			if id == "" {
				continue
			}
			if _, seen := seenToolCalls[id]; seen {
				continue
			}
			seenToolCalls[id] = struct{}{}
			toolOrder = append(toolOrder, id)
		}
	}
	if len(toolOrder) < 2 {
		return messages
	}

	orderSet := make(map[string]struct{}, len(toolOrder))
	for _, id := range toolOrder {
		orderSet[id] = struct{}{}
	}
	resultsByID := make(map[string]StoredMessage, len(toolOrder))
	knownResultCount := 0
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		id := strings.TrimSpace(message.ToolCallID)
		if id == "" {
			id = strings.TrimSpace(message.ToolID)
		}
		if _, ok := orderSet[id]; !ok {
			continue
		}
		resultsByID[id] = message
		knownResultCount++
	}
	if knownResultCount < 2 {
		return messages
	}

	orderedResults := make([]StoredMessage, 0, knownResultCount)
	for _, id := range toolOrder {
		if message, ok := resultsByID[id]; ok {
			orderedResults = append(orderedResults, message)
		}
	}
	if len(orderedResults) != knownResultCount {
		return messages
	}

	out := append([]StoredMessage(nil), messages...)
	cursor := 0
	for index, message := range out {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		id := strings.TrimSpace(message.ToolCallID)
		if id == "" {
			id = strings.TrimSpace(message.ToolID)
		}
		if _, ok := orderSet[id]; !ok {
			continue
		}
		out[index] = orderedResults[cursor]
		cursor++
	}
	return out
}

func canonicalizeStoredToolResultOrderForToolIDs(messages []StoredMessage, toolOrder []string) []StoredMessage {
	if len(messages) < 2 || len(toolOrder) < 2 || storedMessagesContainAssistant(messages) {
		return messages
	}
	orderSet := make(map[string]struct{}, len(toolOrder))
	for _, id := range toolOrder {
		id = strings.TrimSpace(id)
		if id != "" {
			orderSet[id] = struct{}{}
		}
	}
	if len(orderSet) < 2 {
		return messages
	}
	resultsByID := make(map[string]StoredMessage, len(orderSet))
	knownResultCount := 0
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		id := storedToolResultID(message)
		if _, ok := orderSet[id]; !ok {
			continue
		}
		resultsByID[id] = message
		knownResultCount++
	}
	if knownResultCount < 2 {
		return messages
	}
	orderedResults := make([]StoredMessage, 0, knownResultCount)
	for _, id := range toolOrder {
		if message, ok := resultsByID[strings.TrimSpace(id)]; ok {
			orderedResults = append(orderedResults, message)
		}
	}
	if len(orderedResults) != knownResultCount {
		return messages
	}
	out := append([]StoredMessage(nil), messages...)
	cursor := 0
	for index, message := range out {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		if _, ok := orderSet[storedToolResultID(message)]; !ok {
			continue
		}
		out[index] = orderedResults[cursor]
		cursor++
	}
	return out
}

func assistantToolCallOrder(messages []StoredMessage) []string {
	order := make([]string, 0)
	seen := map[string]struct{}{}
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			continue
		}
		for _, call := range message.ToolCalls {
			id := storedToolCallRuntimeID(call)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			order = append(order, id)
		}
	}
	return order
}

func storedToolCallRuntimeID(call StoredToolCall) string {
	for _, id := range []string{call.ToolID, call.ActionID, call.ID} {
		if value := strings.TrimSpace(id); value != "" {
			return value
		}
	}
	return ""
}

func storedToolResultID(message StoredMessage) string {
	for _, id := range []string{message.ToolCallID, message.ToolID, message.ActionID} {
		if value := strings.TrimSpace(id); value != "" {
			return value
		}
	}
	return ""
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

func buildContextWindow(maxSize int, currentSize int, estimatedNextCallSize int) map[string]any {
	cw := map[string]any{}
	if maxSize > 0 {
		cw["maxSize"] = maxSize
	}
	if currentSize > 0 {
		cw["currentSize"] = currentSize
	}
	if estimatedNextCallSize > 0 {
		cw["estimatedNextCallSize"] = estimatedNextCallSize
	}
	if len(cw) == 0 {
		return nil
	}
	return cw
}

func captureStepModelMetadata(modelKey *string, reasoningEffort *string, values ...map[string]any) {
	if modelKey != nil && strings.TrimSpace(*modelKey) == "" {
		for _, value := range values {
			if text := firstStringFromKeys(value, "modelKey"); text != "" {
				*modelKey = text
				break
			}
		}
	}
	if reasoningEffort != nil && strings.TrimSpace(*reasoningEffort) == "" {
		for _, value := range values {
			if text := firstStringFromKeys(value, "reasoningEffort"); text != "" {
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
		firstStringFromKeys(line.Usage, "modelKey"),
		firstStringFromKeys(line.ContextWindow, "modelKey"),
	)
	line.ReasoningEffort = firstNonEmptyStepString(
		line.ReasoningEffort,
		reasoningEffort,
		firstStringFromKeys(line.Usage, "reasoningEffort"),
		firstStringFromKeys(line.ContextWindow, "reasoningEffort"),
	)
	stripStepModelMetadata(line.Usage)
	stripStepModelMetadata(line.ContextWindow)
}

func stripStepModelMetadata(values map[string]any) {
	if values == nil {
		return
	}
	delete(values, "modelKey")
	delete(values, "reasoningEffort")
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

func messagesFromEventValue(value any) []map[string]any {
	if typed, ok := value.([]map[string]any); ok {
		return cloneMessageMaps(typed)
	}
	rawMessages, _ := value.([]any)
	if len(rawMessages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(rawMessages))
	for _, raw := range rawMessages {
		msg, _ := raw.(map[string]any)
		if len(msg) == 0 {
			continue
		}
		out = append(out, cloneStepSystemPayload(msg))
	}
	return out
}

func filterSystemAuditInputMessages(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if isSystemAuditInputMessage(message) {
			continue
		}
		out = append(out, message)
	}
	if len(out) == len(messages) {
		return messages
	}
	return out
}

func isSystemAuditInputMessage(message map[string]any) bool {
	if len(message) == 0 {
		return false
	}
	role, _ := message["role"].(string)
	if !strings.EqualFold(strings.TrimSpace(role), "user") {
		return false
	}
	content := strings.TrimSpace(extractTextFromContent(message["content"]))
	for _, prefix := range []string{
		"[System audit — HITL approval batch]",
		"[System audit — auto approval]",
		"[System audit — approval batch]",
	} {
		if strings.HasPrefix(content, prefix) {
			return true
		}
	}
	return false
}

func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
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
