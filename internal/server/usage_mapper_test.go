package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

func TestUsageCacheTokensKeepsConsistentDetails(t *testing.T) {
	hit, miss := usageCacheTokens(chat.UsageData{
		PromptTokens:          100,
		PromptCacheHitTokens:  40,
		PromptCacheMissTokens: 60,
	})

	if hit != 40 || miss != 60 {
		t.Fatalf("expected consistent cache details to remain unchanged, got hit=%d miss=%d", hit, miss)
	}
}

func TestUsageCacheTokensDerivesMissingCacheMissTokens(t *testing.T) {
	hit, miss := usageCacheTokens(chat.UsageData{
		PromptTokens:         100,
		PromptCacheHitTokens: 40,
	})

	if hit != 40 || miss != 60 {
		t.Fatalf("expected missing cache miss to derive from prompt minus hit, got hit=%d miss=%d", hit, miss)
	}
}

func TestUsageCacheTokensRecomputesInconsistentCacheMissTokens(t *testing.T) {
	hit, miss := usageCacheTokensFromMap(map[string]any{
		"promptTokens": 16929,
		"promptTokensDetails": map[string]any{
			"cacheHitTokens":  8059,
			"cacheMissTokens": 692,
		},
	})

	if hit != 8059 || miss != 8870 {
		t.Fatalf("expected inconsistent cache miss to derive from prompt minus hit, got hit=%d miss=%d", hit, miss)
	}
}

func TestMapUsageDataComputesTimingFields(t *testing.T) {
	mapped := mapUsageData(chat.UsageData{
		CompletionTokens:         50,
		TotalTokens:              50,
		FirstTokenLatencyTotalMs: 2000,
		FirstTokenLatencyCount:   2,
		GenerationDurationMs:     2500,
	})

	if mapped.Timing == nil {
		t.Fatalf("expected timing in mapped usage, got %#v", mapped)
	}
	if mapped.Timing.FirstTokenLatencyMs != 1000 ||
		mapped.Timing.GenerationDurationMs != 2500 ||
		mapped.Timing.OutputTokensPerSecond != 20 {
		t.Fatalf("unexpected mapped timing %#v", mapped.Timing)
	}
}

func TestMapUsageDataFromPayloadComputesTimingFromInternalTotals(t *testing.T) {
	mapped := mapUsageDataFromPayload(map[string]any{
		"completionTokens": 42,
		"totalTokens":      42,
		"timing": map[string]any{
			"firstTokenLatencyTotalMs": 1500,
			"firstTokenLatencyCount":   2,
			"generationDurationMs":     2000,
		},
	})

	if mapped == nil || mapped.Timing == nil {
		t.Fatalf("expected timing in payload usage, got %#v", mapped)
	}
	if mapped.Timing.FirstTokenLatencyMs != 750 ||
		mapped.Timing.GenerationDurationMs != 2000 ||
		mapped.Timing.OutputTokensPerSecond != 21 {
		t.Fatalf("unexpected payload timing %#v", mapped.Timing)
	}
}

func TestLatestChatUsageFromEventsReadsHistoricalUsageSnapshot(t *testing.T) {
	usage := latestChatUsageFromEvents([]stream.EventData{
		{
			Type: "usage.snapshot",
			Payload: map[string]any{
				"usage": map[string]any{
					"current": map[string]any{
						"promptTokens":           100,
						"completionTokens":       50,
						"totalTokens":            150,
						"llmChatCompletionCount": 1,
					},
					"chat": map[string]any{
						"promptTokens":     6574,
						"completionTokens": 104,
						"totalTokens":      6678,
						"completionTokensDetails": map[string]any{
							"reasoningTokens": 70,
						},
						"llmChatCompletionCount": 1,
						"toolCallCount":          3,
					},
				},
			},
		},
	})
	if usage == nil || usage.PromptTokens != 6574 || usage.CompletionTokens != 104 || usage.TotalTokens != 6678 {
		t.Fatalf("expected chat cumulative usage, got %#v", usage)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 70 ||
		usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat cumulative usage, got %#v", usage)
	}
	if usage.ToolCallCount != 3 {
		t.Fatalf("expected chat tool call count, got %#v", usage)
	}
}

func TestChatUsageBreakdownPrefersLatestRunAndHistoricalChatUsage(t *testing.T) {
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 111, CompletionTokens: 22, TotalTokens: 133, LlmChatCompletionCount: 2, ToolCallCount: 4},
		[]chat.RunSummary{
			{RunID: "run-2", Usage: chat.UsageData{ModelKey: "mock-model", PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16, ReasoningTokens: 3, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.12, LlmChatCompletionCount: 1, ToolCallCount: 2}},
			{RunID: "run-1", Usage: chat.UsageData{PromptTokens: 100, CompletionTokens: 17, TotalTokens: 117, LlmChatCompletionCount: 1}},
		},
		chat.ReplayUsage{
			LastRunID: "run-2",
			LastRun:   chat.UsageData{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 111, CompletionTokens: 22, TotalTokens: 133, LlmChatCompletionCount: 2},
		},
		nil, nil, config.BillingConfig{},
	)
	if breakdown == nil || breakdown.LastRun == nil || breakdown.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.PromptTokens != 11 || breakdown.LastRun.CompletionTokens != 5 || breakdown.LastRun.TotalTokens != 16 {
		t.Fatalf("expected latest run usage, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.CompletionTokensDetails == nil || breakdown.LastRun.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("expected latest run usage from run summary, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.ToolCallCount != 2 {
		t.Fatalf("expected latest run tool call count, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.ModelKey != "" || breakdown.LastRun.EstimatedCost == nil || breakdown.LastRun.EstimatedCost.Total != 0.12 {
		t.Fatalf("expected latest run usage to omit modelKey and preserve cost, got %#v", breakdown.LastRun)
	}
	if breakdown.Chat.PromptTokens != 111 || breakdown.Chat.CompletionTokens != 22 || breakdown.Chat.TotalTokens != 133 ||
		breakdown.Chat.LlmChatCompletionCount != 2 || breakdown.Chat.ToolCallCount != 4 {
		t.Fatalf("expected chat cumulative usage, got %#v", breakdown.Chat)
	}
}

func TestChatUsageBreakdownUsesSummaryChatUsageWithoutHistoricalRunFallback(t *testing.T) {
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 30, CompletionTokens: 7, TotalTokens: 37, LlmChatCompletionCount: 2},
		nil,
		chat.ReplayUsage{},
		nil, nil, config.BillingConfig{},
	)
	if breakdown == nil || breakdown.Chat == nil {
		t.Fatalf("expected fallback usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun != nil {
		t.Fatalf("did not expect last run fallback from events, got %#v", breakdown.LastRun)
	}
	if breakdown.Chat.TotalTokens != 37 || breakdown.Chat.LlmChatCompletionCount != 2 {
		t.Fatalf("expected chat fallback from summary, got %#v", breakdown.Chat)
	}
}

func TestChatUsageBreakdownUsesReplayWhenRunHasNoSummary(t *testing.T) {
	breakdown := chatUsageBreakdown(
		nil,
		nil,
		chat.ReplayUsage{
			LastRunID: "run-awaiting",
			LastRun:   chat.UsageData{PromptTokens: 2822, CompletionTokens: 100, TotalTokens: 2922, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 2822, CompletionTokens: 100, TotalTokens: 2922, LlmChatCompletionCount: 1},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.LastRun == nil || breakdown.Chat == nil {
		t.Fatalf("expected replay usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.PromptTokens != 2822 || breakdown.LastRun.CompletionTokens != 100 ||
		breakdown.LastRun.TotalTokens != 2922 || breakdown.LastRun.LlmChatCompletionCount != 1 {
		t.Fatalf("unexpected replay last run usage %#v", breakdown.LastRun)
	}
	if breakdown.Chat.PromptTokens != 2822 || breakdown.Chat.CompletionTokens != 100 ||
		breakdown.Chat.TotalTokens != 2922 || breakdown.Chat.LlmChatCompletionCount != 1 {
		t.Fatalf("unexpected replay chat usage %#v", breakdown.Chat)
	}
}

func TestChatUsageBreakdownPrefersCompletedRunSummaryOverReplayForSameRun(t *testing.T) {
	breakdown := chatUsageBreakdown(
		nil,
		[]chat.RunSummary{
			{
				RunID: "run-complete",
				Usage: chat.UsageData{
					ModelKey:               "mock-model",
					PromptTokens:           10,
					CompletionTokens:       5,
					TotalTokens:            15,
					ReasoningTokens:        3,
					EstimatedCostCurrency:  "CNY",
					EstimatedCostTotal:     0.12,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-complete",
			LastRun:   chat.UsageData{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, LlmChatCompletionCount: 1},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.ModelKey != "" ||
		breakdown.LastRun.EstimatedCost == nil ||
		breakdown.LastRun.EstimatedCost.Total != 0.12 ||
		breakdown.LastRun.CompletionTokensDetails == nil ||
		breakdown.LastRun.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("expected completed run summary to omit model and preserve cost/details, got %#v", breakdown.LastRun)
	}
}

func TestMapChatContextWindowIncludesWindowSizes(t *testing.T) {
	contextWindow := mapChatContextWindow(map[string]any{
		"maxSize":               128000,
		"currentSize":           100,
		"estimatedNextCallSize": 200,
		"modelKey":              "mock-model",
		"reasoningEffort":       "HIGH",
	})

	if contextWindow == nil ||
		contextWindow.MaxSize != 128000 ||
		contextWindow.CurrentSize != 100 ||
		contextWindow.EstimatedNextCallSize != 200 ||
		contextWindow.ModelKey != "mock-model" ||
		contextWindow.ReasoningEffort != "HIGH" {
		t.Fatalf("unexpected context window %#v", contextWindow)
	}
}

func TestChatUsageBreakdownUsesReplayChatWhenSummaryLags(t *testing.T) {
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10, LlmChatCompletionCount: 1},
		nil,
		chat.ReplayUsage{
			LastRunID: "run-2",
			LastRun:   chat.UsageData{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 15, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 18, CompletionTokens: 7, TotalTokens: 25, LlmChatCompletionCount: 2},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.Chat.PromptTokens != 18 || breakdown.Chat.CompletionTokens != 7 ||
		breakdown.Chat.TotalTokens != 25 || breakdown.Chat.LlmChatCompletionCount != 2 {
		t.Fatalf("expected replay chat usage to replace stale summary, got %#v", breakdown.Chat)
	}
}

func TestChatUsageBreakdownSupplementsCostFromReplayWhenSummaryLacksCost(t *testing.T) {
	breakdown := chatUsageBreakdown(
		&chat.UsageData{
			PromptTokens:           100,
			CompletionTokens:       50,
			TotalTokens:            150,
			ReasoningTokens:        8,
			CachedTokens:           30,
			PromptCacheHitTokens:   30,
			LlmChatCompletionCount: 1,
			ToolCallCount:          3,
		},
		nil,
		chat.ReplayUsage{
			LastRunID: "run-replay",
			LastRun:   chat.UsageData{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, LlmChatCompletionCount: 1, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.06},
			Chat:      chat.UsageData{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, LlmChatCompletionCount: 1, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.06},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.Chat.EstimatedCost == nil || breakdown.Chat.EstimatedCost.Total != 0.06 {
		t.Fatalf("expected chat estimated cost from replay, got %#v", breakdown.Chat)
	}
	if breakdown.Chat.EstimatedCost.Currency != "CNY" {
		t.Fatalf("expected chat cost currency CNY, got %q", breakdown.Chat.EstimatedCost.Currency)
	}
	if breakdown.Chat.ToolCallCount != 3 {
		t.Fatalf("expected chat ToolCallCount from summary (3), got %d", breakdown.Chat.ToolCallCount)
	}
	if breakdown.Chat.CompletionTokensDetails == nil || breakdown.Chat.CompletionTokensDetails.ReasoningTokens != 8 {
		t.Fatalf("expected chat ReasoningTokens from summary, got %#v", breakdown.Chat.CompletionTokensDetails)
	}
	if breakdown.Chat.PromptTokensDetails == nil || breakdown.Chat.PromptTokensDetails.CacheHitTokens != 30 {
		t.Fatalf("expected chat cache hit tokens from summary, got %#v", breakdown.Chat.PromptTokensDetails)
	}
	if breakdown.LastRun == nil || breakdown.LastRun.EstimatedCost == nil || breakdown.LastRun.EstimatedCost.Total != 0.06 {
		t.Fatalf("expected last run estimated cost from replay, got %#v", breakdown.LastRun)
	}
}

func TestChatUsageBreakdownPrefersRunSummaryCostOverReplay(t *testing.T) {
	breakdown := chatUsageBreakdown(
		nil,
		[]chat.RunSummary{
			{
				RunID: "run-complete",
				Usage: chat.UsageData{
					ModelKey:               "mock-model",
					PromptTokens:           10,
					CompletionTokens:       5,
					TotalTokens:            15,
					ReasoningTokens:        3,
					EstimatedCostCurrency:  "CNY",
					EstimatedCostTotal:     0.12,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-complete",
			LastRun:   chat.UsageData{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, LlmChatCompletionCount: 1, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.12},
			Chat:      chat.UsageData{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, LlmChatCompletionCount: 1, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.06},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost == nil || breakdown.LastRun.EstimatedCost.Total != 0.12 {
		t.Fatalf("expected run summary cost 0.12, got %#v", breakdown.LastRun.EstimatedCost)
	}
}

func TestChatUsageBreakdownSupplementsRunCostFromReplayWhenRunSummaryLacksCost(t *testing.T) {
	breakdown := chatUsageBreakdown(
		nil,
		[]chat.RunSummary{
			{
				RunID: "run-no-cost",
				Usage: chat.UsageData{
					PromptTokens:           10,
					CompletionTokens:       5,
					TotalTokens:            15,
					ReasoningTokens:        3,
					CachedTokens:           8,
					PromptCacheHitTokens:   8,
					LlmChatCompletionCount: 1,
					ToolCallCount:          2,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-no-cost",
			LastRun:   chat.UsageData{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, LlmChatCompletionCount: 1, EstimatedCostCurrency: "USD", EstimatedCostTotal: 0.035},
			Chat:      chat.UsageData{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, LlmChatCompletionCount: 1},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost == nil || breakdown.LastRun.EstimatedCost.Total != 0.035 {
		t.Fatalf("expected run cost from replay (0.035), got %#v", breakdown.LastRun.EstimatedCost)
	}
	if breakdown.LastRun.EstimatedCost.Currency != "USD" {
		t.Fatalf("expected run cost currency USD, got %q", breakdown.LastRun.EstimatedCost.Currency)
	}
	if breakdown.LastRun.ToolCallCount != 2 {
		t.Fatalf("expected run ToolCallCount from summary (2), got %d", breakdown.LastRun.ToolCallCount)
	}
	if breakdown.LastRun.CompletionTokensDetails == nil || breakdown.LastRun.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("expected run ReasoningTokens from summary, got %#v", breakdown.LastRun.CompletionTokensDetails)
	}
	if breakdown.LastRun.PromptTokensDetails == nil || breakdown.LastRun.PromptTokensDetails.CacheHitTokens != 8 {
		t.Fatalf("expected run cache hit tokens from summary, got %#v", breakdown.LastRun.PromptTokensDetails)
	}
}

func TestChatUsageBreakdownReplayOnlyAwaitingRunReturnsCost(t *testing.T) {
	breakdown := chatUsageBreakdown(
		nil,
		nil,
		chat.ReplayUsage{
			LastRunID: "run-awaiting",
			LastRun:   chat.UsageData{PromptTokens: 500, CompletionTokens: 100, TotalTokens: 600, LlmChatCompletionCount: 1, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.25},
			Chat:      chat.UsageData{PromptTokens: 500, CompletionTokens: 100, TotalTokens: 600, LlmChatCompletionCount: 1, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.25},
		},
		nil, nil, config.BillingConfig{},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost == nil || breakdown.LastRun.EstimatedCost.Total != 0.25 {
		t.Fatalf("expected awaiting run cost 0.25, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.EstimatedCost.Currency != "CNY" {
		t.Fatalf("expected awaiting run cost currency CNY, got %q", breakdown.LastRun.EstimatedCost.Currency)
	}
	if breakdown.Chat == nil || breakdown.Chat.EstimatedCost == nil || breakdown.Chat.EstimatedCost.Total != 0.25 {
		t.Fatalf("expected chat cost 0.25, got %#v", breakdown.Chat)
	}
}

// ---------------------------------------------------------------------------
// No read-time cost fallback tests
// ---------------------------------------------------------------------------

func TestChatUsageBreakdownDoesNotEstimateLastRunWithoutPersistedCost(t *testing.T) {
	registry := writeTestModelRegistry(t)
	breakdown := chatUsageBreakdown(
		nil,
		[]chat.RunSummary{
			{
				RunID: "run-no-cost",
				Usage: chat.UsageData{
					ModelKey:               "mock-model",
					PromptTokens:           1_000_000,
					CompletionTokens:       1_000_000,
					TotalTokens:            2_000_000,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-no-cost",
			LastRun:   chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
		},
		map[string]any{"modelKey": "mock-model"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)
	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("did not expect read-time lastRun estimatedCost, got %#v", breakdown.LastRun.EstimatedCost)
	}
}

func TestChatUsageBreakdownDoesNotEstimateChatWhenTokenSameAsLastRun(t *testing.T) {
	registry := writeTestModelRegistry(t)
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
		[]chat.RunSummary{
			{
				RunID: "run-single",
				Usage: chat.UsageData{
					ModelKey:               "mock-model",
					PromptTokens:           1_000_000,
					CompletionTokens:       1_000_000,
					TotalTokens:            2_000_000,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-single",
			LastRun:   chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
		},
		map[string]any{"modelKey": "mock-model"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)

	if breakdown == nil || breakdown.LastRun == nil || breakdown.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("did not expect read-time lastRun estimatedCost, got %#v", breakdown.LastRun.EstimatedCost)
	}
	if breakdown.Chat.EstimatedCost != nil {
		t.Fatalf("did not expect read-time chat estimatedCost, got %#v", breakdown.Chat.EstimatedCost)
	}
}

func TestChatUsageBreakdownDoesNotEstimateMultiRunChat(t *testing.T) {
	registry := writeTestModelRegistry(t)
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300, LlmChatCompletionCount: 2},
		[]chat.RunSummary{
			{
				RunID: "run-last",
				Usage: chat.UsageData{
					ModelKey:               "mock-model",
					PromptTokens:           100,
					CompletionTokens:       50,
					TotalTokens:            150,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{},
		map[string]any{"modelKey": "mock-model"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)

	if breakdown == nil || breakdown.LastRun == nil || breakdown.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("did not expect read-time lastRun estimatedCost, got %#v", breakdown.LastRun.EstimatedCost)
	}
	if breakdown.Chat.EstimatedCost != nil {
		t.Fatalf("did not expect read-time chat estimatedCost, got %#v", breakdown.Chat.EstimatedCost)
	}
}

func TestChatUsageBreakdownSkipsFallbackWhenModelNotFound(t *testing.T) {
	registry := writeTestModelRegistry(t)
	breakdown := chatUsageBreakdown(
		nil,
		[]chat.RunSummary{
			{
				RunID: "run-unknown-model",
				Usage: chat.UsageData{
					ModelKey:               "nonexistent-model",
					PromptTokens:           1000,
					CompletionTokens:       500,
					TotalTokens:            1500,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-unknown-model",
			LastRun:   chat.UsageData{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, LlmChatCompletionCount: 1},
		},
		map[string]any{"modelKey": "nonexistent-model"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("expected no estimatedCost when model not found, got %#v", breakdown.LastRun.EstimatedCost)
	}
}

func TestChatUsageBreakdownSkipsFallbackWhenPricingMissing(t *testing.T) {
	registry := writeTestModelRegistryNoPricing(t)
	breakdown := chatUsageBreakdown(
		nil,
		[]chat.RunSummary{
			{
				RunID: "run-no-pricing",
				Usage: chat.UsageData{
					ModelKey:               "no-pricing-model",
					PromptTokens:           1000,
					CompletionTokens:       500,
					TotalTokens:            1500,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-no-pricing",
			LastRun:   chat.UsageData{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, LlmChatCompletionCount: 1},
		},
		map[string]any{"modelKey": "no-pricing-model"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("expected no estimatedCost when pricing missing, got %#v", breakdown.LastRun.EstimatedCost)
	}
}

func TestChatUsageBreakdownDoesNotFallbackToCompletedRunModel(t *testing.T) {
	registry := writeTestModelRegistry(t)
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
		[]chat.RunSummary{
			{
				RunID: "run-old-model",
				Usage: chat.UsageData{
					ModelKey:               "old-model",
					PromptTokens:           1_000_000,
					CompletionTokens:       1_000_000,
					TotalTokens:            2_000_000,
					LlmChatCompletionCount: 1,
				},
			},
		},
		chat.ReplayUsage{
			LastRunID: "run-old-model",
			LastRun:   chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
		},
		map[string]any{"modelKey": "new-model"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("did not expect completed-run read-time fallback cost, got %#v", breakdown.LastRun.EstimatedCost)
	}
	if breakdown.Chat == nil {
		t.Fatalf("expected chat usage, got %#v", breakdown)
	}
	if breakdown.Chat.EstimatedCost != nil {
		t.Fatalf("did not expect chat read-time fallback cost, got %#v", breakdown.Chat.EstimatedCost)
	}
}

func TestChatUsageBreakdownDoesNotFallbackToContextWindowForReplayOnlyModel(t *testing.T) {
	registry := writeTestModelRegistry(t)
	breakdown := chatUsageBreakdown(
		nil,
		nil,
		chat.ReplayUsage{
			LastRunID: "run-active",
			LastRun:   chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
			Chat:      chat.UsageData{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000, LlmChatCompletionCount: 1},
		},
		map[string]any{"modelKey": "th-deepseek-v4-pro"},
		registry,
		config.BillingConfig{Currency: "CNY"},
	)

	if breakdown == nil || breakdown.LastRun == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.EstimatedCost != nil {
		t.Fatalf("did not expect context-window read-time fallback cost, got %#v", breakdown.LastRun.EstimatedCost)
	}
	if breakdown.Chat == nil {
		t.Fatalf("expected chat usage, got %#v", breakdown)
	}
	if breakdown.Chat.EstimatedCost != nil {
		t.Fatalf("did not expect chat context-window read-time fallback cost, got %#v", breakdown.Chat.EstimatedCost)
	}
}

// ---------------------------------------------------------------------------
// Helpers: write model registries for tests
// ---------------------------------------------------------------------------

func writeTestModelRegistry(t *testing.T) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "providers"), filepath.Join(root, "models")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir registry dir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: https://example.com",
		"apiKey: test",
		"defaultModel: mock-model",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "mock.yml"), []byte(strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.025",
		"  inputCacheMiss: 3.00",
		"  output: 6.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "old.yml"), []byte(strings.Join([]string{
		"key: old-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: old-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.00",
		"  inputCacheMiss: 1.00",
		"  output: 2.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write old model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "new.yml"), []byte(strings.Join([]string{
		"key: new-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: new-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.00",
		"  inputCacheMiss: 10.00",
		"  output: 20.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write new model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "th-deepseek-v4-pro.yml"), []byte(strings.Join([]string{
		"key: th-deepseek-v4-pro",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: th-deepseek-v4-pro",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.00",
		"  inputCacheMiss: 5.00",
		"  output: 8.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write th deepseek model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}

func writeTestModelRegistryNoPricing(t *testing.T) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "providers"), filepath.Join(root, "models")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir registry dir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: https://example.com",
		"apiKey: test",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "no-pricing.yml"), []byte(strings.Join([]string{
		"key: no-pricing-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: no-pricing-model-id",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}
