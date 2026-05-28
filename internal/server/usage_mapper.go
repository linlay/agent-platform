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
		PromptCacheHitTokens:   usage.PromptCacheHitTokens,
		PromptCacheMissTokens:  usage.PromptCacheMissTokens,
		LlmChatCompletionCount: usage.LlmChatCompletionCount,
	}
	if usage.CachedTokens > 0 {
		out.PromptTokensDetails = &api.PromptTokenDetails{CachedTokens: usage.CachedTokens}
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
		PromptCacheHitTokens:   contracts.AnyIntNode(usage["promptCacheHitTokens"]),
		PromptCacheMissTokens:  contracts.AnyIntNode(usage["promptCacheMissTokens"]),
		LlmChatCompletionCount: contracts.AnyIntNode(usage["llmChatCompletionCount"]),
	}
	if details, _ := usage["promptTokensDetails"].(map[string]any); details != nil {
		if cachedTokens := contracts.AnyIntNode(details["cachedTokens"]); cachedTokens > 0 {
			out.PromptTokensDetails = &api.PromptTokenDetails{CachedTokens: cachedTokens}
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
