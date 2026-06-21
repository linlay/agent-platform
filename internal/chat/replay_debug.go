package chat

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/stream"
)

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

func isNewFormat(lines []map[string]any) bool {
	for _, line := range lines {
		if _, ok := line["_type"]; ok {
			return true
		}
		if _, ok := line["type"]; ok {
			return false
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Step line → SSE events reconstruction
// ---------------------------------------------------------------------------

func synthesizedUsageSnapshotContextWindow(contextWindow map[string]any) map[string]any {
	cw := map[string]any{}
	if len(contextWindow) == 0 {
		return cw
	}
	if v := toIntFromKeys(contextWindow, "maxSize"); v > 0 {
		cw["maxSize"] = v
	}
	if v := toIntFromKeys(contextWindow, "currentSize"); v > 0 {
		cw["currentSize"] = v
	}
	if v := toIntFromKeys(contextWindow, "estimatedNextCallSize"); v > 0 {
		cw["estimatedNextCallSize"] = v
	}
	if modelKey := firstStringFromKeys(contextWindow, "modelKey"); modelKey != "" {
		cw["modelKey"] = modelKey
	}
	if reasoningEffort := firstStringFromKeys(contextWindow, "reasoningEffort"); reasoningEffort != "" {
		cw["reasoningEffort"] = reasoningEffort
	}
	return cw
}

func contextWindowWithStepModel(line map[string]any, contextWindow map[string]any, usage map[string]any) map[string]any {
	if len(contextWindow) == 0 {
		contextWindow = map[string]any{}
	} else {
		contextWindow = cloneStringAnyMap(contextWindow)
	}
	modelKey := firstNonEmptyStepString(
		firstStringFromKeys(line, "modelKey"),
		firstStringFromKeys(contextWindow, "modelKey"),
		firstStringFromKeys(usage, "modelKey"),
	)
	if modelKey != "" {
		contextWindow["modelKey"] = modelKey
	}
	reasoningEffort := firstNonEmptyStepString(
		firstStringFromKeys(line, "reasoningEffort"),
		firstStringFromKeys(contextWindow, "reasoningEffort"),
		firstStringFromKeys(usage, "reasoningEffort"),
	)
	if reasoningEffort != "" {
		contextWindow["reasoningEffort"] = reasoningEffort
	}
	return contextWindow
}

func cumulativeUsagePayload(cumulative map[string]int) map[string]any {
	if cumulative == nil {
		return map[string]any{"promptTokens": 0, "completionTokens": 0, "totalTokens": 0}
	}
	out := map[string]any{
		"promptTokens":     cumulative["promptTokens"],
		"completionTokens": cumulative["completionTokens"],
		"totalTokens":      cumulative["totalTokens"],
	}
	addUsageDetailsToMap(out, cumulative["cachedTokens"], cumulative["reasoningTokens"], cumulative["promptCacheHitTokens"], cumulative["promptCacheMissTokens"])
	if count := cumulative["llmChatCompletionCount"]; count > 0 {
		out["llmChatCompletionCount"] = count
	}
	return out
}

func synthesizeUsageSnapshotEvent(runID, chatID string, taskID string, usage map[string]any, runCumulative, chatCumulative map[string]int, contextWindow map[string]any, ts int64, nextSeq func() int64) *stream.EventData {
	if !hasProviderUsagePayload(usage) {
		return nil
	}
	currentUsage := usagePayloadFromMap(usage, false)
	if modelKey := firstStringFromKeys(currentUsage, "modelKey"); modelKey == "" {
		if modelKey := firstStringFromKeys(contextWindow, "modelKey"); modelKey != "" {
			currentUsage["modelKey"] = modelKey
		}
	}
	if reasoningEffort := firstStringFromKeys(currentUsage, "reasoningEffort"); reasoningEffort == "" {
		if reasoningEffort := firstStringFromKeys(contextWindow, "reasoningEffort"); reasoningEffort != "" {
			currentUsage["reasoningEffort"] = reasoningEffort
		}
	}
	payload := map[string]any{
		"runId":  runID,
		"chatId": chatID,
		"usage": map[string]any{
			"current": currentUsage,
		},
	}
	if cw := synthesizedUsageSnapshotContextWindow(contextWindow); len(cw) > 0 {
		payload["contextWindow"] = cw
	}
	if strings.TrimSpace(taskID) != "" {
		payload["taskId"] = taskID
	}
	return &stream.EventData{
		Seq:       nextSeq(),
		Type:      "usage.snapshot",
		Timestamp: ts,
		Payload:   payload,
	}
}

func applyUsageModelMetadataFromContextWindow(usage map[string]any, contextWindow map[string]any) {
	if usage == nil {
		return
	}
	if firstStringFromKeys(usage, "modelKey") == "" {
		if modelKey := firstStringFromKeys(contextWindow, "modelKey"); modelKey != "" {
			usage["modelKey"] = modelKey
		}
	}
	if firstStringFromKeys(usage, "reasoningEffort") == "" {
		if reasoningEffort := firstStringFromKeys(contextWindow, "reasoningEffort"); reasoningEffort != "" {
			usage["reasoningEffort"] = reasoningEffort
		}
	}
}

func usagePayloadFromSnapshotEvent(event stream.EventData, usage map[string]any, includeLLMChatCompletionCount bool) map[string]any {
	out := usagePayloadFromMap(usage, includeLLMChatCompletionCount)
	contextWindow, _ := event.Value("contextWindow").(map[string]any)
	if _, ok := out["modelKey"]; !ok {
		model, _ := event.Value("model").(map[string]any)
		modelKey := firstStringFromKeys(contextWindow, "modelKey")
		if modelKey == "" {
			modelKey = strings.TrimSpace(stringFromAny(model["key"]))
		}
		if modelKey != "" {
			out["modelKey"] = modelKey
		}
	}
	if _, ok := out["reasoningEffort"]; !ok {
		model, _ := event.Value("model").(map[string]any)
		reasoningEffort := firstStringFromKeys(contextWindow, "reasoningEffort")
		if reasoningEffort == "" {
			reasoningEffort = strings.TrimSpace(stringFromAny(model["reasoningEffort"]))
		}
		if reasoningEffort != "" {
			out["reasoningEffort"] = reasoningEffort
		}
	}
	return out
}

func usagePayloadFromMap(usage map[string]any, includeLLMChatCompletionCount bool) map[string]any {
	out := map[string]any{
		"promptTokens":     toIntFromKeys(usage, "promptTokens"),
		"completionTokens": toIntFromKeys(usage, "completionTokens"),
		"totalTokens":      toIntFromKeys(usage, "totalTokens"),
	}
	if modelKey := firstStringFromKeys(usage, "modelKey"); modelKey != "" {
		out["modelKey"] = modelKey
	}
	if reasoningEffort := firstStringFromKeys(usage, "reasoningEffort"); reasoningEffort != "" {
		out["reasoningEffort"] = reasoningEffort
	}
	addUsageDetailsToMap(
		out,
		usageCacheHitTokensFromMap(usage),
		toNestedIntFromKeys(usage, "completionTokensDetails", "reasoningTokens"),
		usageCacheHitTokensFromMap(usage),
		usageCacheMissTokensFromMap(usage),
	)
	if includeLLMChatCompletionCount {
		if count := toIntFromKeys(usage, "llmChatCompletionCount"); count > 0 {
			out["llmChatCompletionCount"] = count
		} else if hasLLMTokenUsagePayload(usage) {
			out["llmChatCompletionCount"] = 1
		}
	}
	if count := toIntFromKeys(usage, "toolCallCount"); count > 0 {
		out["toolCallCount"] = count
	}
	if estimatedCost, ok := usage["estimatedCost"].(map[string]any); ok && len(estimatedCost) > 0 {
		out["estimatedCost"] = cloneStringAnyMap(estimatedCost)
	}
	return out
}

func firstStringFromKeys(values map[string]any, keys ...string) string {
	if values == nil {
		return ""
	}
	for _, key := range keys {
		if text := strings.TrimSpace(stringFromAny(values[key])); text != "" {
			return text
		}
	}
	return ""
}

func hasProviderUsagePayload(usage map[string]any) bool {
	if len(usage) == 0 {
		return false
	}
	return hasLLMTokenUsagePayload(usage) ||
		toIntFromKeys(usage, "toolCallCount") > 0
}

func hasLLMTokenUsagePayload(usage map[string]any) bool {
	if len(usage) == 0 {
		return false
	}
	return toIntFromKeys(usage, "promptTokens") > 0 ||
		toIntFromKeys(usage, "completionTokens") > 0 ||
		toIntFromKeys(usage, "totalTokens") > 0 ||
		usageCacheHitTokensFromMap(usage) > 0 ||
		toNestedIntFromKeys(usage, "completionTokensDetails", "reasoningTokens") > 0 ||
		usageCacheMissTokensFromMap(usage) > 0
}

func addUsageDetailsToMap(out map[string]any, cachedTokens int, reasoningTokens int, promptCacheHitTokens int, promptCacheMissTokens int) {
	cacheHitTokens := promptCacheHitTokens
	if cacheHitTokens <= 0 {
		cacheHitTokens = cachedTokens
	}
	promptDetails := map[string]any{}
	if cacheHitTokens > 0 {
		promptDetails["cacheHitTokens"] = cacheHitTokens
	}
	if promptCacheMissTokens > 0 {
		promptDetails["cacheMissTokens"] = promptCacheMissTokens
	} else if promptTokens := toIntFromKeys(out, "promptTokens"); cacheHitTokens > 0 && promptTokens > cacheHitTokens {
		promptDetails["cacheMissTokens"] = promptTokens - cacheHitTokens
	}
	if len(promptDetails) > 0 {
		out["promptTokensDetails"] = promptDetails
	}
	if reasoningTokens > 0 {
		out["completionTokensDetails"] = map[string]any{"reasoningTokens": reasoningTokens}
	}
}

func toIntValue(v any) int {
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

func toIntFromKeys(values map[string]any, keys ...string) int {
	if values == nil {
		return 0
	}
	for _, key := range keys {
		if v := toIntValue(values[key]); v > 0 {
			return v
		}
	}
	return 0
}

func toNestedIntFromKeys(values map[string]any, detailKey string, valueKey string) int {
	if values == nil {
		return 0
	}
	details, _ := values[detailKey].(map[string]any)
	if v := toIntFromKeys(details, valueKey); v > 0 {
		return v
	}
	return 0
}

func usageCacheHitTokensFromMap(usage map[string]any) int {
	if usage == nil {
		return 0
	}
	if v := toNestedIntFromKeys(usage, "promptTokensDetails", "cacheHitTokens"); v > 0 {
		return v
	}
	if v := toNestedIntFromKeys(usage, "promptTokensDetails", "cachedTokens"); v > 0 {
		return v
	}
	return toIntFromKeys(usage, "promptCacheHitTokens")
}

func usageCacheMissTokensFromMap(usage map[string]any) int {
	if usage == nil {
		return 0
	}
	if v := toNestedIntFromKeys(usage, "promptTokensDetails", "cacheMissTokens"); v > 0 {
		return v
	}
	if v := toIntFromKeys(usage, "promptCacheMissTokens"); v > 0 {
		return v
	}
	promptTokens := toIntFromKeys(usage, "promptTokens")
	cacheHitTokens := usageCacheHitTokensFromMap(usage)
	if cacheHitTokens > 0 && promptTokens > cacheHitTokens {
		return promptTokens - cacheHitTokens
	}
	return 0
}

func int64FromAny(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
