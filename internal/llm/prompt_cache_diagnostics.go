package llm

import (
	"math"
	"strings"
)

func buildPromptCacheDiagnostics(messages []openAIMessage, usage *openAIUsage) map[string]any {
	if usage == nil || usage.PromptTokens <= 0 {
		return nil
	}
	hitTokens := usage.PromptCacheHitTokens
	if hitTokens <= 0 {
		hitTokens = usage.PromptTokensDetails.CachedTokens
	}
	missTokens := usage.PromptCacheMissTokens
	if hitTokens <= 0 && missTokens <= 0 {
		return nil
	}

	lastUserIdx := -1
	lastAssistantIdx := -1
	for idx, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "user" {
			lastUserIdx = idx
		}
		if role == "assistant" {
			lastAssistantIdx = idx
		}
	}

	systemTokens := 0
	historyTokens := 0
	currentUserTokens := 0
	lastAssistantTokens := 0
	newTailTokens := 0
	for idx, msg := range messages {
		tokens := estimateTokensFromOpenAIMessage(msg)
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch {
		case role == "system":
			systemTokens += tokens
		case role == "user" && idx == lastUserIdx:
			currentUserTokens += tokens
		default:
			historyTokens += tokens
		}
		if idx == lastAssistantIdx {
			lastAssistantTokens = tokens
		}
		if lastAssistantIdx >= 0 && idx >= lastAssistantIdx {
			newTailTokens += tokens
		}
	}

	return map[string]any{
		"promptTokens":    usage.PromptTokens,
		"cacheHitTokens":  hitTokens,
		"cacheMissTokens": missTokens,
		"hitRatePercent":  percentage(hitTokens, usage.PromptTokens),
		"missRatePercent": percentage(missTokens, usage.PromptTokens),
		"primaryReason":   promptCacheMissReason(missTokens, newTailTokens, lastAssistantTokens),
		"estimated": map[string]any{
			"systemTokens":        systemTokens,
			"historyTokens":       historyTokens,
			"currentUserTokens":   currentUserTokens,
			"lastAssistantTokens": lastAssistantTokens,
			"newTailTokens":       newTailTokens,
		},
	}
}

func promptCacheMissReason(missTokens int, newTailTokens int, lastAssistantTokens int) string {
	switch {
	case missTokens <= 0:
		return "none"
	case lastAssistantTokens > 0 && missTokens <= maxCacheDiagnosticInt(newTailTokens*2, lastAssistantTokens):
		return "new_tail_from_previous_turn"
	case newTailTokens > 0:
		return "new_tail_or_provider_granularity"
	default:
		return "provider_reported_aggregate_only"
	}
}

func percentage(part int, total int) float64 {
	if part <= 0 || total <= 0 {
		return 0
	}
	return math.Round((float64(part)/float64(total))*1000) / 10
}

func maxCacheDiagnosticInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
