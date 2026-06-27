package server

import (
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

func mapUsageDataPtr(usage *chat.UsageData) *api.ChatUsageData {
	if usage == nil || !usageDataHasPublicValue(*usage) {
		return nil
	}
	mapped := mapUsageData(*usage)
	return &mapped
}

func usageDataHasPublicValue(usage chat.UsageData) bool {
	return usage.TotalTokens > 0 ||
		usage.PromptTokens > 0 ||
		usage.CompletionTokens > 0 ||
		usage.LlmChatCompletionCount > 0 ||
		usage.ToolCallCount > 0 ||
		strings.TrimSpace(usage.EstimatedCostCurrency) != "" ||
		usage.FirstTokenLatencyTotalMs > 0 ||
		usage.FirstTokenLatencyCount > 0 ||
		usage.GenerationDurationMs > 0
}

func mapUsageData(usage chat.UsageData) api.ChatUsageData {
	out := api.ChatUsageData{
		ModelKey:               strings.TrimSpace(usage.ModelKey),
		PromptTokens:           usage.PromptTokens,
		CompletionTokens:       usage.CompletionTokens,
		TotalTokens:            usage.TotalTokens,
		LlmChatCompletionCount: usage.LlmChatCompletionCount,
		ToolCallCount:          usage.ToolCallCount,
	}
	if cacheHitTokens, cacheMissTokens := usageCacheTokens(usage); cacheHitTokens > 0 || cacheMissTokens > 0 {
		out.PromptTokensDetails = &api.PromptTokenDetails{
			CacheHitTokens:  cacheHitTokens,
			CacheMissTokens: cacheMissTokens,
		}
	}
	if usage.ReasoningTokens > 0 {
		out.CompletionTokensDetails = &api.CompletionTokenDetails{ReasoningTokens: usage.ReasoningTokens}
	}
	if strings.TrimSpace(usage.EstimatedCostCurrency) != "" {
		out.EstimatedCost = &api.EstimatedCost{
			Currency:       usage.EstimatedCostCurrency,
			InputCacheHit:  usage.EstimatedCostInputHit,
			InputCacheMiss: usage.EstimatedCostInputMiss,
			Output:         usage.EstimatedCostOutput,
			Total:          usage.EstimatedCostTotal,
		}
	}
	if timing := apiTimingFromUsageData(usage); timing != nil {
		out.Timing = timing
	}
	return out
}

func latestChatUsageFromEvents(events []stream.EventData) *api.ChatUsageData {
	return latestUsageFromEvents(events, "chat")
}

func latestRunUsageFromEvents(events []stream.EventData) *api.ChatUsageData {
	return latestUsageFromEvents(events, "run")
}

func latestUsageFromEvents(events []stream.EventData, key string) *api.ChatUsageData {
	var latest *api.ChatUsageData
	for _, event := range events {
		if event.Type != "usage.snapshot" && event.Type != "run.complete" && event.Type != "run.error" {
			continue
		}
		usage, _ := event.Value("usage").(map[string]any)
		selected, _ := usage[key].(map[string]any)
		if selected == nil {
			continue
		}
		if mapped := mapUsageDataFromPayload(selected); mapped != nil {
			latest = mapped
		}
	}
	return latest
}

func chatUsageBreakdown(summaryUsage *chat.UsageData, runs []chat.RunSummary, replayUsage chat.ReplayUsage, contextWindow map[string]any, models *models.ModelRegistry, billing config.BillingConfig) *api.ChatUsageBreakdown {
	lastRun, _ := latestRunUsageWithModelFromSummaries(runs)
	if replayRunID := strings.TrimSpace(replayUsage.LastRunID); replayRunID != "" {
		if completedRun, _, foundCompletedRun := runUsageWithModelForID(runs, replayRunID); foundCompletedRun {
			if completedRun != nil {
				if completedRun.EstimatedCost != nil {
					lastRun = completedRun
				} else if strings.TrimSpace(replayUsage.LastRun.EstimatedCostCurrency) != "" {
					applyEstimatedCost(completedRun, replayUsage.LastRun)
					lastRun = completedRun
				} else {
					lastRun = completedRun
				}
			} else if mapped := mapUsageDataPtr(&replayUsage.LastRun); mapped != nil {
				lastRun = mapped
			}
		} else if mapped := mapUsageDataPtr(&replayUsage.LastRun); mapped != nil {
			lastRun = mapped
		}
	}

	chatUsage := mapUsageDataPtr(summaryUsage)
	if replayChatUsageIsNewer(replayUsage.Chat, summaryUsage) {
		chatUsage = mapUsageDataPtr(&replayUsage.Chat)
	} else if replayChatCostShouldSupplement(replayUsage.Chat, summaryUsage) {
		if chatUsage != nil {
			applyEstimatedCost(chatUsage, replayUsage.Chat)
		} else {
			chatUsage = mapUsageDataPtr(&replayUsage.Chat)
		}
	}

	if lastRun == nil && chatUsage == nil {
		return nil
	}
	return &api.ChatUsageBreakdown{
		LastRun: lastRun,
		Chat:    chatUsage,
	}
}

func applyEstimatedCost(target *api.ChatUsageData, source chat.UsageData) {
	if strings.TrimSpace(source.EstimatedCostCurrency) == "" {
		return
	}
	target.EstimatedCost = &api.EstimatedCost{
		Currency:       source.EstimatedCostCurrency,
		InputCacheHit:  source.EstimatedCostInputHit,
		InputCacheMiss: source.EstimatedCostInputMiss,
		Output:         source.EstimatedCostOutput,
		Total:          source.EstimatedCostTotal,
	}
}

func replayChatCostShouldSupplement(replay chat.UsageData, summary *chat.UsageData) bool {
	if strings.TrimSpace(replay.EstimatedCostCurrency) == "" {
		return false
	}
	if mapUsageDataPtr(summary) == nil {
		return false
	}
	if summary.EstimatedCostCurrency != "" {
		return false
	}
	return replay.TotalTokens >= summary.TotalTokens
}

func latestRunUsageFromSummaries(runs []chat.RunSummary) *api.ChatUsageData {
	usage, _ := latestRunUsageWithModelFromSummaries(runs)
	return usage
}

func latestRunUsageWithModelFromSummaries(runs []chat.RunSummary) (*api.ChatUsageData, string) {
	for _, run := range runs {
		usage := run.Usage
		modelKey := strings.TrimSpace(usage.ModelKey)
		usage.ModelKey = ""
		if mapped := mapUsageDataPtr(&usage); mapped != nil {
			return mapped, modelKey
		}
	}
	return nil, ""
}

func runUsageForID(runs []chat.RunSummary, runID string) *api.ChatUsageData {
	usage, _, _ := runUsageWithModelForID(runs, runID)
	return usage
}

func runUsageWithModelForID(runs []chat.RunSummary, runID string) (*api.ChatUsageData, string, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, "", false
	}
	for _, run := range runs {
		if strings.TrimSpace(run.RunID) != runID {
			continue
		}
		usage := run.Usage
		modelKey := strings.TrimSpace(usage.ModelKey)
		usage.ModelKey = ""
		if mapped := mapUsageDataPtr(&usage); mapped != nil {
			return mapped, modelKey, true
		}
		return nil, modelKey, true
	}
	return nil, "", false
}

func replayChatUsageIsNewer(replay chat.UsageData, summary *chat.UsageData) bool {
	if mapUsageDataPtr(&replay) == nil {
		return false
	}
	if mapUsageDataPtr(summary) == nil {
		return true
	}
	return replay.PromptTokens > summary.PromptTokens ||
		replay.CompletionTokens > summary.CompletionTokens ||
		replay.TotalTokens > summary.TotalTokens ||
		replay.CachedTokens > summary.CachedTokens ||
		replay.ReasoningTokens > summary.ReasoningTokens ||
		replay.PromptCacheHitTokens > summary.PromptCacheHitTokens ||
		replay.PromptCacheMissTokens > summary.PromptCacheMissTokens ||
		replay.LlmChatCompletionCount > summary.LlmChatCompletionCount ||
		replay.ToolCallCount > summary.ToolCallCount ||
		replay.FirstTokenLatencyTotalMs > summary.FirstTokenLatencyTotalMs ||
		replay.FirstTokenLatencyCount > summary.FirstTokenLatencyCount ||
		replay.GenerationDurationMs > summary.GenerationDurationMs
}

func mapChatContextWindow(contextWindow map[string]any) *api.ChatContextWindow {
	if len(contextWindow) == 0 {
		return nil
	}
	out := api.ChatContextWindow{
		MaxSize:               contracts.AnyIntNode(contextWindow["maxSize"]),
		CurrentSize:           contracts.AnyIntNode(contextWindow["currentSize"]),
		EstimatedNextCallSize: contracts.AnyIntNode(contextWindow["estimatedNextCallSize"]),
		ModelKey:              strings.TrimSpace(contracts.AnyStringNode(contextWindow["modelKey"])),
		ReasoningEffort:       strings.TrimSpace(contracts.AnyStringNode(contextWindow["reasoningEffort"])),
	}
	if out.MaxSize == 0 && out.CurrentSize == 0 && out.EstimatedNextCallSize == 0 {
		return nil
	}
	return &out
}

func mapUsageDataFromPayload(usage map[string]any) *api.ChatUsageData {
	if usage == nil {
		return nil
	}
	out := api.ChatUsageData{
		ModelKey:               strings.TrimSpace(contracts.AnyStringNode(usage["modelKey"])),
		PromptTokens:           contracts.AnyIntNode(usage["promptTokens"]),
		CompletionTokens:       contracts.AnyIntNode(usage["completionTokens"]),
		TotalTokens:            contracts.AnyIntNode(usage["totalTokens"]),
		LlmChatCompletionCount: contracts.AnyIntNode(usage["llmChatCompletionCount"]),
		ToolCallCount:          contracts.AnyIntNode(usage["toolCallCount"]),
	}
	if cacheHitTokens, cacheMissTokens := usageCacheTokensFromMap(usage); cacheHitTokens > 0 || cacheMissTokens > 0 {
		out.PromptTokensDetails = &api.PromptTokenDetails{
			CacheHitTokens:  cacheHitTokens,
			CacheMissTokens: cacheMissTokens,
		}
	}
	if details, _ := usage["completionTokensDetails"].(map[string]any); details != nil {
		if reasoningTokens := contracts.AnyIntNode(details["reasoningTokens"]); reasoningTokens > 0 {
			out.CompletionTokensDetails = &api.CompletionTokenDetails{ReasoningTokens: reasoningTokens}
		}
	}
	if estimatedCost := apiEstimatedCostFromMap(usage); estimatedCost != nil {
		out.EstimatedCost = estimatedCost
	}
	if timing := apiTimingFromUsageMap(usage, out.CompletionTokens); timing != nil {
		out.Timing = timing
	}
	if out.TotalTokens == 0 && out.LlmChatCompletionCount == 0 && out.ToolCallCount == 0 && out.Timing == nil {
		return nil
	}
	return &out
}

func apiTimingFromUsageData(usage chat.UsageData) *api.ChatUsageTiming {
	var firstTokenLatencyMs int64
	if usage.FirstTokenLatencyTotalMs > 0 && usage.FirstTokenLatencyCount > 0 {
		firstTokenLatencyMs = usage.FirstTokenLatencyTotalMs / int64(usage.FirstTokenLatencyCount)
	}
	return apiTimingFromValues(firstTokenLatencyMs, usage.GenerationDurationMs, usage.CompletionTokens)
}

func apiTimingFromUsageMap(usage map[string]any, completionTokens int) *api.ChatUsageTiming {
	timing, _ := usage["timing"].(map[string]any)
	if timing == nil {
		return nil
	}
	firstTokenLatencyMs := int64(contracts.AnyIntNode(timing["firstTokenLatencyMs"]))
	if firstTokenLatencyMs <= 0 {
		total := int64(contracts.AnyIntNode(timing["firstTokenLatencyTotalMs"]))
		count := contracts.AnyIntNode(timing["firstTokenLatencyCount"])
		if total > 0 && count > 0 {
			firstTokenLatencyMs = total / int64(count)
		}
	}
	generationDurationMs := int64(contracts.AnyIntNode(timing["generationDurationMs"]))
	return apiTimingFromValues(firstTokenLatencyMs, generationDurationMs, completionTokens)
}

func apiTimingFromValues(firstTokenLatencyMs int64, generationDurationMs int64, completionTokens int) *api.ChatUsageTiming {
	if firstTokenLatencyMs <= 0 && generationDurationMs <= 0 {
		return nil
	}
	out := &api.ChatUsageTiming{
		FirstTokenLatencyMs:  firstTokenLatencyMs,
		GenerationDurationMs: generationDurationMs,
	}
	if completionTokens > 0 && generationDurationMs > 0 {
		out.OutputTokensPerSecond = float64(completionTokens) * 1000 / float64(generationDurationMs)
	}
	return out
}

func apiEstimatedCostFromMap(usage map[string]any) *api.EstimatedCost {
	estimatedCost, _ := usage["estimatedCost"].(map[string]any)
	if estimatedCost == nil {
		return nil
	}
	currency := strings.TrimSpace(contracts.AnyStringNode(estimatedCost["currency"]))
	if currency == "" {
		return nil
	}
	return &api.EstimatedCost{
		Currency:       currency,
		InputCacheHit:  floatValue(estimatedCost["inputCacheHit"]),
		InputCacheMiss: floatValue(estimatedCost["inputCacheMiss"]),
		Output:         floatValue(estimatedCost["output"]),
		Total:          floatValue(estimatedCost["total"]),
	}
}

func usageCacheTokens(usage chat.UsageData) (int, int) {
	cacheHitTokens := usage.PromptCacheHitTokens
	if cacheHitTokens <= 0 {
		cacheHitTokens = usage.CachedTokens
	}
	cacheMissTokens := usage.PromptCacheMissTokens
	return normalizeUsageCacheTokens(usage.PromptTokens, cacheHitTokens, cacheMissTokens)
}

func usageCacheTokensFromMap(usage map[string]any) (int, int) {
	details, _ := usage["promptTokensDetails"].(map[string]any)
	cacheHitTokens := firstPositiveInt(
		contracts.AnyIntNode(details["cacheHitTokens"]),
	)
	cacheMissTokens := firstPositiveInt(
		contracts.AnyIntNode(details["cacheMissTokens"]),
	)
	promptTokens := contracts.AnyIntNode(usage["promptTokens"])
	return normalizeUsageCacheTokens(promptTokens, cacheHitTokens, cacheMissTokens)
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
