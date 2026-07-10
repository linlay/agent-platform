package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	. "agent-platform/internal/contracts"
)

func (s *llmRunStream) buildLLMRequestDelta(prepared preparedProviderRequest, effectiveToolChoice string) DeltaLLMRequest {
	systemRef := s.currentSystemRefForCall(prepared, effectiveToolChoice)
	inputMessages := s.currentInputMessagesForJSONL()
	s.pendingSteerInputs = nil
	return DeltaLLMRequest{
		TaskID:          strings.TrimSpace(s.session.SubTaskID),
		ChatID:          strings.TrimSpace(s.session.ChatID),
		Model:           s.currentModelSnapshot(prepared),
		ModelKey:        strings.TrimSpace(s.model.Key),
		ReasoningEffort: s.effectiveReasoningEffort(),
		System:          s.currentInlineSystemSnapshot(prepared, effectiveToolChoice, systemRef),
		SystemRef:       systemRef,
		ToolChoice:      strings.TrimSpace(effectiveToolChoice),
		RequestOptions:  requestOptionsFromPreparedBody(prepared.RequestBody),
		InputMessages:   inputMessages,
	}
}

func (s *llmRunStream) currentModelSnapshot(prepared preparedProviderRequest) map[string]any {
	model := map[string]any{}
	if key := strings.TrimSpace(s.model.Key); key != "" {
		model["key"] = key
	}
	if id := strings.TrimSpace(s.model.ModelID); id != "" {
		model["id"] = id
	}
	if providerKey := strings.TrimSpace(s.provider.Key); providerKey != "" {
		model["providerKey"] = providerKey
	}
	protocol := strings.TrimSpace(s.model.Protocol)
	if protocol == "" {
		protocol = "OPENAI"
	}
	if protocol != "" {
		model["protocol"] = protocol
	}
	if endpoint := strings.TrimSpace(prepared.Endpoint); endpoint != "" {
		model["endpoint"] = endpoint
	}
	if reasoningEffort := s.effectiveReasoningEffort(); reasoningEffort != "" {
		model["reasoningEffort"] = reasoningEffort
	}
	if len(model) == 0 {
		return nil
	}
	return model
}

func requestOptionsFromPreparedBody(body map[string]any) map[string]any {
	if len(body) == 0 {
		return nil
	}
	out := make(map[string]any, len(body))
	for key, value := range body {
		switch key {
		case "messages", "tools", "tool_choice", "model", "system":
			continue
		default:
			out[key] = value
		}
	}
	return cloneAnyMapViaJSON(out)
}

func (s *llmRunStream) currentInlineSystemSnapshot(prepared preparedProviderRequest, effectiveToolChoice string, systemRef map[string]any) map[string]any {
	if len(systemRef) > 0 {
		return nil
	}
	systemMessage := firstSystemMessageSnapshot(s.messages)
	if len(systemMessage) == 0 && len(s.toolSpecs) == 0 {
		return nil
	}
	out := map[string]any{
		"agentKey":      strings.TrimSpace(s.session.AgentKey),
		"cacheKey":      s.currentSystemCacheKey(),
		"systemMessage": systemMessage,
		"tools":         openAIToolSpecsToAny(s.toolSpecs),
	}
	if model := s.currentModelSnapshot(prepared); len(model) > 0 {
		out["model"] = model
	}
	if toolChoice := strings.TrimSpace(effectiveToolChoice); toolChoice != "" {
		out["toolChoice"] = toolChoice
	}
	if requestOptions := requestOptionsFromPreparedBody(prepared.RequestBody); len(requestOptions) > 0 {
		out["requestOptions"] = requestOptions
	}
	out["fingerprint"] = fingerprintLLMCallProfile(out)
	return out
}

func (s *llmRunStream) currentSystemCacheKey() string {
	if s == nil {
		return ""
	}
	cacheKey := strings.TrimSpace(s.systemInitCacheKey)
	if cacheKey == "" {
		cacheKey = SystemInitCacheKey(s.session.Mode, s.promptBuildOptions.Stage)
	}
	return cacheKey
}

func fingerprintLLMCallProfile(profile map[string]any) string {
	if len(profile) == 0 {
		return ""
	}
	payload := cloneAnyMapViaJSON(profile)
	delete(payload, "fingerprint")
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func firstSystemMessageSnapshot(messages []openAIMessage) map[string]any {
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "system" {
			continue
		}
		raw := rawMessageFromOpenAIMessage(message)
		if len(raw) == 0 {
			return nil
		}
		return cloneAnyMapViaJSON(raw)
	}
	return nil
}

func rawMessageFromOpenAIMessage(message openAIMessage) map[string]any {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		return nil
	}
	raw := map[string]any{"role": role}
	if content, ok := message.Content.(string); ok && strings.TrimSpace(content) != "" {
		raw["content"] = content
	} else if message.Content != nil {
		raw["content"] = message.Content
	}
	if strings.TrimSpace(message.Name) != "" {
		raw["name"] = strings.TrimSpace(message.Name)
	}
	if strings.TrimSpace(message.ToolCallID) != "" {
		raw["tool_call_id"] = message.ToolCallID
	}
	if len(message.ToolCalls) > 0 {
		calls := make([]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, map[string]any{
				"id":   call.ID,
				"type": firstNonBlank(call.Type, "function"),
				"function": map[string]any{
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				},
			})
		}
		raw["tool_calls"] = calls
	}
	if strings.TrimSpace(message.ReasoningContent) != "" {
		raw["reasoning_content"] = message.ReasoningContent
	}
	return raw
}

func (s *llmRunStream) currentInputMessagesForJSONL() []map[string]any {
	raw := trailingUserMessages(s.messages)
	if len(raw) == 0 {
		return nil
	}
	raw = filterSystemAuditInputMessages(raw)
	if len(raw) == 0 {
		return nil
	}
	raw = dropPendingSteerInputMessages(raw, s.pendingSteerInputs)
	if len(raw) == 0 {
		return nil
	}
	if messageSlicesEqual(raw, s.session.CurrentMessages) {
		return nil
	}
	return raw
}

func dropPendingSteerInputMessages(messages []map[string]any, pendingSteers []map[string]any) []map[string]any {
	if len(messages) == 0 || len(pendingSteers) == 0 {
		return messages
	}
	out := make([]map[string]any, 0, len(messages))
	steerIndex := 0
	for _, message := range messages {
		if steerIndex < len(pendingSteers) && messageMapsEqual(message, pendingSteers[steerIndex]) {
			steerIndex++
			continue
		}
		out = append(out, message)
	}
	if len(out) == len(messages) {
		return messages
	}
	return out
}

func trailingUserMessages(messages []openAIMessage) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	start := len(messages)
	for start > 0 {
		role := strings.TrimSpace(messages[start-1].Role)
		if role != "user" {
			break
		}
		start--
	}
	if start == len(messages) {
		return nil
	}
	out := make([]map[string]any, 0, len(messages)-start)
	for _, message := range messages[start:] {
		raw := rawMessageFromOpenAIMessage(message)
		if len(raw) > 0 {
			out = append(out, cloneAnyMapViaJSON(raw))
		}
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
	content := strings.TrimSpace(inputMessageContentText(message["content"]))
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

func inputMessageContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var builder strings.Builder
		for _, item := range typed {
			part, _ := item.(map[string]any)
			if text, _ := part["text"].(string); text != "" {
				builder.WriteString(text)
			}
		}
		return builder.String()
	default:
		return ""
	}
}

func messageSlicesEqual(left []map[string]any, right []map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !messageMapsEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

func messageMapsEqual(left map[string]any, right map[string]any) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftData) == string(rightData)
}

func cloneAnyMapViaJSON(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return CloneMap(values)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return CloneMap(values)
	}
	return out
}
