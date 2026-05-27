package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func mapUsageDataPtr(usage *chat.UsageData) *api.ChatUsageData {
	if usage == nil || (usage.TotalTokens == 0 && usage.LlmChatCompletionCount == 0) {
		return nil
	}
	mapped := mapUsageData(*usage)
	return &mapped
}

func mapUsageData(usage chat.UsageData) api.ChatUsageData {
	out := api.ChatUsageData{
		PromptTokens:           usage.PromptTokens,
		CompletionTokens:       usage.CompletionTokens,
		TotalTokens:            usage.TotalTokens,
		LlmChatCompletionCount: usage.LlmChatCompletionCount,
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

func chatUsageBreakdown(summaryUsage *chat.UsageData, runs []chat.RunSummary, events []stream.EventData) *api.ChatUsageBreakdown {
	var lastRun *api.ChatUsageData
	for _, run := range runs {
		if mapped := mapUsageDataPtr(&run.Usage); mapped != nil {
			lastRun = mapped
			break
		}
	}

	chatUsage := mapUsageDataPtr(summaryUsage)

	if lastRun == nil && chatUsage == nil {
		return nil
	}
	return &api.ChatUsageBreakdown{
		LastRun: lastRun,
		Chat:    chatUsage,
	}
}

func mapUsageDataFromPayload(usage map[string]any) *api.ChatUsageData {
	if usage == nil {
		return nil
	}
	out := api.ChatUsageData{
		PromptTokens:           contracts.AnyIntNode(usage["promptTokens"]),
		CompletionTokens:       contracts.AnyIntNode(usage["completionTokens"]),
		TotalTokens:            contracts.AnyIntNode(usage["totalTokens"]),
		LlmChatCompletionCount: contracts.AnyIntNode(usage["llmChatCompletionCount"]),
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
	if out.TotalTokens == 0 && out.LlmChatCompletionCount == 0 {
		return nil
	}
	return &out
}

func usageCacheTokens(usage chat.UsageData) (int, int) {
	cacheHitTokens := usage.PromptCacheHitTokens
	if cacheHitTokens <= 0 {
		cacheHitTokens = usage.CachedTokens
	}
	cacheMissTokens := usage.PromptCacheMissTokens
	if cacheMissTokens <= 0 && cacheHitTokens > 0 && usage.PromptTokens > cacheHitTokens {
		cacheMissTokens = usage.PromptTokens - cacheHitTokens
	}
	return cacheHitTokens, cacheMissTokens
}

func usageCacheTokensFromMap(usage map[string]any) (int, int) {
	details, _ := usage["promptTokensDetails"].(map[string]any)
	if details == nil {
		details, _ = usage["prompt_tokens_details"].(map[string]any)
	}
	cacheHitTokens := firstPositiveInt(
		contracts.AnyIntNode(details["cacheHitTokens"]),
		contracts.AnyIntNode(details["cache_hit_tokens"]),
		contracts.AnyIntNode(details["cachedTokens"]),
		contracts.AnyIntNode(details["cached_tokens"]),
		contracts.AnyIntNode(usage["promptCacheHitTokens"]),
		contracts.AnyIntNode(usage["prompt_cache_hit_tokens"]),
	)
	cacheMissTokens := firstPositiveInt(
		contracts.AnyIntNode(details["cacheMissTokens"]),
		contracts.AnyIntNode(details["cache_miss_tokens"]),
		contracts.AnyIntNode(usage["promptCacheMissTokens"]),
		contracts.AnyIntNode(usage["prompt_cache_miss_tokens"]),
	)
	if cacheMissTokens <= 0 {
		promptTokens := firstPositiveInt(
			contracts.AnyIntNode(usage["promptTokens"]),
			contracts.AnyIntNode(usage["prompt_tokens"]),
		)
		if cacheHitTokens > 0 && promptTokens > cacheHitTokens {
			cacheMissTokens = promptTokens - cacheHitTokens
		}
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
