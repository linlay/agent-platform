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
	return DeltaLLMRequest{
		TaskID:          strings.TrimSpace(s.session.SubTaskID),
		ChatID:          strings.TrimSpace(s.session.ChatID),
		Model:           s.currentModelSnapshot(prepared),
		ModelKey:        strings.TrimSpace(s.model.Key),
		ReasoningEffort: s.effectiveReasoningEffort(),
		SystemRef:       systemRef,
		ToolChoice:      strings.TrimSpace(effectiveToolChoice),
		RequestOptions:  requestOptionsFromPreparedBody(prepared.RequestBody),
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
		// The affinity key is derived from Chat ID on send, not persisted as
		// part of the system profile or per-turn JSONL request options.
		case "messages", "input", "instructions", "tools", "tool_choice", "model", "system", "prompt_cache_key":
			continue
		default:
			out[key] = value
		}
	}
	return cloneAnyMapViaJSON(out)
}

func (s *llmRunStream) currentSystemCacheKey() string {
	if s == nil {
		return ""
	}
	cacheKey := strings.TrimSpace(s.systemInitCacheKey)
	if cacheKey == "" {
		cacheKey = sessionSystemInitCacheKey(s.session, s.promptBuildOptions.Stage)
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
