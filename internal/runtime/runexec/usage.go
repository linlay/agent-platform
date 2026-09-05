// Package runexec owns local run execution and terminal accounting.
package runexec

import (
	"encoding/json"
	"strconv"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func UsageDataFromMap(usage map[string]any) chat.UsageData {
	out := chat.UsageData{
		ModelKey:               strings.TrimSpace(contracts.AnyStringNode(usage["modelKey"])),
		PromptTokens:           contracts.AnyIntNode(usage["promptTokens"]),
		CompletionTokens:       contracts.AnyIntNode(usage["completionTokens"]),
		TotalTokens:            contracts.AnyIntNode(usage["totalTokens"]),
		ReasoningTokens:        UsageDetailInt(usage, "completionTokensDetails", "reasoningTokens"),
		LlmChatCompletionCount: contracts.AnyIntNode(usage["llmChatCompletionCount"]),
		ToolCallCount:          contracts.AnyIntNode(usage["toolCallCount"]),
	}
	cacheHitTokens, cacheMissTokens := usageCacheTokensFromMap(usage)
	out.CachedTokens = cacheHitTokens
	out.PromptCacheHitTokens = cacheHitTokens
	out.PromptCacheMissTokens = cacheMissTokens
	if estimatedCost := EstimatedCostFromMap(usage); estimatedCost != nil {
		out.EstimatedCostCurrency = strings.ToUpper(strings.TrimSpace(contracts.AnyStringNode(estimatedCost["currency"])))
		out.EstimatedCostInputHit = FloatValue(estimatedCost["inputCacheHit"])
		out.EstimatedCostInputMiss = FloatValue(estimatedCost["inputCacheMiss"])
		out.EstimatedCostOutput = FloatValue(estimatedCost["output"])
		out.EstimatedCostTotal = FloatValue(estimatedCost["total"])
	}
	ApplyUsageTimingFromMap(&out, usage)
	return out
}

func MergeUsageMapIntoRunData(target *chat.UsageData, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	incoming := UsageDataFromMap(usage)
	MergeRunUsageData(target, incoming)
}

func MergeRunUsageData(target *chat.UsageData, incoming chat.UsageData) {
	if target == nil {
		return
	}
	modelKey := target.ModelKey
	currency := target.EstimatedCostCurrency
	inputHit := target.EstimatedCostInputHit
	inputMiss := target.EstimatedCostInputMiss
	output := target.EstimatedCostOutput
	total := target.EstimatedCostTotal
	firstTokenLatencyTotalMs := target.FirstTokenLatencyTotalMs
	firstTokenLatencyCount := target.FirstTokenLatencyCount
	generationDurationMs := target.GenerationDurationMs
	*target = incoming
	if strings.TrimSpace(incoming.ModelKey) == "" {
		target.ModelKey = modelKey
	}
	if strings.TrimSpace(incoming.EstimatedCostCurrency) == "" {
		target.EstimatedCostCurrency = currency
		target.EstimatedCostInputHit = inputHit
		target.EstimatedCostInputMiss = inputMiss
		target.EstimatedCostOutput = output
		target.EstimatedCostTotal = total
	}
	if incoming.FirstTokenLatencyTotalMs == 0 && incoming.FirstTokenLatencyCount == 0 && incoming.GenerationDurationMs == 0 {
		target.FirstTokenLatencyTotalMs = firstTokenLatencyTotalMs
		target.FirstTokenLatencyCount = firstTokenLatencyCount
		target.GenerationDurationMs = generationDurationMs
	}
}

func AddEstimatedUsageCost(target *chat.UsageData, delta chat.UsageData) {
	if target == nil || strings.TrimSpace(delta.EstimatedCostCurrency) == "" {
		return
	}
	if strings.TrimSpace(target.EstimatedCostCurrency) == "" {
		target.EstimatedCostCurrency = strings.ToUpper(strings.TrimSpace(delta.EstimatedCostCurrency))
	}
	target.EstimatedCostInputHit += delta.EstimatedCostInputHit
	target.EstimatedCostInputMiss += delta.EstimatedCostInputMiss
	target.EstimatedCostOutput += delta.EstimatedCostOutput
	target.EstimatedCostTotal += delta.EstimatedCostTotal
}

func EstimatedCostFromMap(usage map[string]any) map[string]any {
	estimatedCost, _ := usage["estimatedCost"].(map[string]any)
	return estimatedCost
}

func FloatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func UsageDetailInt(usage map[string]any, detailKey string, valueKey string) int {
	details, _ := usage[detailKey].(map[string]any)
	return contracts.AnyIntNode(details[valueKey])
}

func ApplyUsageTimingFromMap(target *chat.UsageData, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	timing, _ := usage["timing"].(map[string]any)
	if timing == nil {
		return
	}
	firstTokenLatencyTotalMs := int64(contracts.AnyIntNode(timing["firstTokenLatencyTotalMs"]))
	firstTokenLatencyCount := contracts.AnyIntNode(timing["firstTokenLatencyCount"])
	if firstTokenLatencyTotalMs <= 0 || firstTokenLatencyCount <= 0 {
		if firstTokenLatencyMs := int64(contracts.AnyIntNode(timing["firstTokenLatencyMs"])); firstTokenLatencyMs > 0 {
			firstTokenLatencyTotalMs = firstTokenLatencyMs
			firstTokenLatencyCount = 1
		}
	}
	target.FirstTokenLatencyTotalMs = firstTokenLatencyTotalMs
	target.FirstTokenLatencyCount = firstTokenLatencyCount
	target.GenerationDurationMs = int64(contracts.AnyIntNode(timing["generationDurationMs"]))
}

func AddUsageData(base chat.UsageData, delta chat.UsageData) chat.UsageData {
	return chat.UsageData{
		ModelKey:                 MergedUsageModelKey(base, delta),
		PromptTokens:             base.PromptTokens + delta.PromptTokens,
		CompletionTokens:         base.CompletionTokens + delta.CompletionTokens,
		TotalTokens:              base.TotalTokens + delta.TotalTokens,
		CachedTokens:             base.CachedTokens + delta.CachedTokens,
		ReasoningTokens:          base.ReasoningTokens + delta.ReasoningTokens,
		PromptCacheHitTokens:     base.PromptCacheHitTokens + delta.PromptCacheHitTokens,
		PromptCacheMissTokens:    base.PromptCacheMissTokens + delta.PromptCacheMissTokens,
		EstimatedCostCurrency:    firstNonBlank(base.EstimatedCostCurrency, delta.EstimatedCostCurrency),
		EstimatedCostInputHit:    base.EstimatedCostInputHit + delta.EstimatedCostInputHit,
		EstimatedCostInputMiss:   base.EstimatedCostInputMiss + delta.EstimatedCostInputMiss,
		EstimatedCostOutput:      base.EstimatedCostOutput + delta.EstimatedCostOutput,
		EstimatedCostTotal:       base.EstimatedCostTotal + delta.EstimatedCostTotal,
		LlmChatCompletionCount:   base.LlmChatCompletionCount + delta.LlmChatCompletionCount,
		ToolCallCount:            base.ToolCallCount + delta.ToolCallCount,
		FirstTokenLatencyTotalMs: base.FirstTokenLatencyTotalMs + delta.FirstTokenLatencyTotalMs,
		FirstTokenLatencyCount:   base.FirstTokenLatencyCount + delta.FirstTokenLatencyCount,
		GenerationDurationMs:     base.GenerationDurationMs + delta.GenerationDurationMs,
	}
}

func AddUsageTimingMap(out map[string]any, usage chat.UsageData) {
	if out == nil {
		return
	}
	timing := map[string]any{}
	if usage.FirstTokenLatencyCount > 0 {
		timing["firstTokenLatencyTotalMs"] = usage.FirstTokenLatencyTotalMs
		timing["firstTokenLatencyCount"] = usage.FirstTokenLatencyCount
	}
	if usage.GenerationDurationMs > 0 {
		timing["generationDurationMs"] = usage.GenerationDurationMs
	}
	if len(timing) > 0 {
		out["timing"] = timing
	}
}

func UsageDataMap(usage chat.UsageData) map[string]any {
	return UsageDataMapWithOptions(usage, false)
}

func UsageDataMapForSnapshot(usage chat.UsageData) map[string]any {
	return UsageDataMapWithOptions(usage, true)
}

func UsageDataMapWithOptions(usage chat.UsageData, includeZeroToolCallCount bool) map[string]any {
	out := map[string]any{
		"promptTokens":     usage.PromptTokens,
		"completionTokens": usage.CompletionTokens,
		"totalTokens":      usage.TotalTokens,
	}
	if modelKey := strings.TrimSpace(usage.ModelKey); modelKey != "" {
		out["modelKey"] = modelKey
	}
	if usage.CachedTokens > 0 {
		out["promptTokensDetails"] = map[string]any{"cacheHitTokens": usage.CachedTokens}
	}
	if usage.ReasoningTokens > 0 || includeZeroToolCallCount {
		out["completionTokensDetails"] = map[string]any{"reasoningTokens": usage.ReasoningTokens}
	}
	cacheHitTokens, cacheMissTokens := usageCacheTokens(usage)
	if cacheHitTokens > 0 || cacheMissTokens > 0 {
		promptDetails, _ := out["promptTokensDetails"].(map[string]any)
		if promptDetails == nil {
			promptDetails = map[string]any{}
			out["promptTokensDetails"] = promptDetails
		}
		if cacheHitTokens > 0 || includeZeroToolCallCount {
			promptDetails["cacheHitTokens"] = cacheHitTokens
		}
		if cacheMissTokens > 0 || includeZeroToolCallCount {
			promptDetails["cacheMissTokens"] = cacheMissTokens
		}
	}
	if usage.LlmChatCompletionCount > 0 {
		out["llmChatCompletionCount"] = usage.LlmChatCompletionCount
	}
	if usage.ToolCallCount > 0 || includeZeroToolCallCount {
		out["toolCallCount"] = usage.ToolCallCount
	}
	if estimated := UsageEstimatedCostFromData(usage); estimated != nil {
		out["estimatedCost"] = estimated
	}
	AddUsageTimingMap(out, usage)
	return out
}

func MergedUsageModelKey(base chat.UsageData, delta chat.UsageData) string {
	baseKey := strings.TrimSpace(base.ModelKey)
	deltaKey := strings.TrimSpace(delta.ModelKey)
	if baseKey == "" && !UsageHasData(base) {
		return deltaKey
	}
	if deltaKey == "" && !UsageHasData(delta) {
		return baseKey
	}
	if baseKey != "" && baseKey == deltaKey {
		return baseKey
	}
	return ""
}

func UsageHasData(usage chat.UsageData) bool {
	return usage.TotalTokens > 0 || usage.PromptTokens > 0 || usage.CompletionTokens > 0 ||
		usage.LlmChatCompletionCount > 0 || usage.ToolCallCount > 0 ||
		usage.EstimatedCostTotal > 0 || strings.TrimSpace(usage.EstimatedCostCurrency) != "" ||
		usage.FirstTokenLatencyTotalMs > 0 || usage.FirstTokenLatencyCount > 0 || usage.GenerationDurationMs > 0
}

func UsageEstimatedCostFromData(usage chat.UsageData) map[string]any {
	if strings.TrimSpace(usage.EstimatedCostCurrency) == "" {
		return nil
	}
	return map[string]any{
		"currency":       strings.ToUpper(strings.TrimSpace(usage.EstimatedCostCurrency)),
		"inputCacheHit":  usage.EstimatedCostInputHit,
		"inputCacheMiss": usage.EstimatedCostInputMiss,
		"output":         usage.EstimatedCostOutput,
		"total":          usage.EstimatedCostTotal,
	}
}

func usageCacheTokens(usage chat.UsageData) (int, int) {
	cacheHitTokens := usage.PromptCacheHitTokens
	if cacheHitTokens <= 0 {
		cacheHitTokens = usage.CachedTokens
	}
	return normalizeUsageCacheTokens(usage.PromptTokens, cacheHitTokens, usage.PromptCacheMissTokens)
}

func usageCacheTokensFromMap(usage map[string]any) (int, int) {
	details, _ := usage["promptTokensDetails"].(map[string]any)
	cacheHitTokens := firstPositiveInt(contracts.AnyIntNode(details["cacheHitTokens"]))
	cacheMissTokens := firstPositiveInt(contracts.AnyIntNode(details["cacheMissTokens"]))
	return normalizeUsageCacheTokens(contracts.AnyIntNode(usage["promptTokens"]), cacheHitTokens, cacheMissTokens)
}

func normalizeUsageCacheTokens(promptTokens int, cacheHitTokens int, cacheMissTokens int) (int, int) {
	if cacheHitTokens <= 0 || promptTokens <= 0 || promptTokens < cacheHitTokens {
		return cacheHitTokens, cacheMissTokens
	}
	derivedMissTokens := promptTokens - cacheHitTokens
	if cacheMissTokens <= 0 || cacheHitTokens+cacheMissTokens != promptTokens {
		cacheMissTokens = derivedMissTokens
	}
	return cacheHitTokens, cacheMissTokens
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
