package llm

import (
	"encoding/json"
	"strings"

	. "agent-platform/internal/contracts"
)

func (s *llmRunStream) buildLLMRequestDelta(prepared preparedProviderRequest, effectiveToolChoice string) DeltaLLMRequest {
	return DeltaLLMRequest{
		TaskID:          strings.TrimSpace(s.session.SubTaskID),
		ChatID:          strings.TrimSpace(s.session.ChatID),
		Model:           s.currentModelSnapshot(prepared),
		ModelKey:        strings.TrimSpace(s.model.Key),
		ReasoningEffort: s.effectiveReasoningEffort(),
		System:          s.currentInlineSystemSnapshot(),
		SystemRef:       s.currentSystemRef(),
		ToolChoice:      strings.TrimSpace(effectiveToolChoice),
		RequestOptions:  requestOptionsFromPreparedBody(prepared.RequestBody),
		InputMessages:   s.currentInputMessagesForJSONL(),
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

func (s *llmRunStream) currentInlineSystemSnapshot() map[string]any {
	if len(s.currentSystemRef()) > 0 {
		return nil
	}
	systemMessage := firstSystemMessageSnapshot(s.messages)
	if len(systemMessage) == 0 && len(s.toolSpecs) == 0 {
		return nil
	}
	return map[string]any{
		"systemMessage": systemMessage,
		"tools":         openAIToolSpecsToAny(s.toolSpecs),
	}
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

func (s *llmRunStream) currentInputMessagesForJSONL() []map[string]any {
	raw := trailingUserMessages(s.messages)
	if len(raw) == 0 {
		return nil
	}
	if messageSlicesEqual(raw, s.session.CurrentMessages) {
		return nil
	}
	return raw
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

func messageSlicesEqual(left []map[string]any, right []map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
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
