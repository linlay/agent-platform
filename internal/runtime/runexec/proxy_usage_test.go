package runexec

import (
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestProxyUsageTrackerDecoratesUsageSnapshotWithEstimatedCost(t *testing.T) {
	runUsage := chat.UsageData{}
	tracker := NewProxyUsageTracker(
		chat.UsageData{
			PromptTokens:             10,
			CompletionTokens:         5,
			TotalTokens:              15,
			LlmChatCompletionCount:   1,
			FirstTokenLatencyTotalMs: 500,
			FirstTokenLatencyCount:   1,
			GenerationDurationMs:     500,
		},
		&runUsage,
		writeUsageCostRegistry(t),
		config.BillingConfig{Currency: "CNY"},
	)
	event := &stream.EventData{
		Type: "usage.snapshot",
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"modelKey":         "mock-model",
					"promptTokensDetails": map[string]any{
						"cacheHitTokens":  200_000,
						"cacheMissTokens": 800_000,
					},
				},
				"run": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"timing": map[string]any{
						"firstTokenLatencyMs":  2000,
						"generationDurationMs": 2500,
					},
				},
			},
		},
	}

	tracker.Decorate(event)

	usage, _ := event.Payload["usage"].(map[string]any)
	current, _ := usage["current"].(map[string]any)
	currentCost, _ := current["estimatedCost"].(map[string]any)
	if current["modelKey"] != "mock-model" || FloatValue(currentCost["total"]) != 8.405 {
		t.Fatalf("expected proxy current cost decoration, got %#v", current)
	}
	run, _ := usage["run"].(map[string]any)
	runCost, _ := run["estimatedCost"].(map[string]any)
	if FloatValue(runCost["total"]) != 8.405 {
		t.Fatalf("expected proxy run cost from current usage, got %#v", run)
	}
	runTiming, _ := run["timing"].(map[string]any)
	if contracts.AnyIntNode(runTiming["firstTokenLatencyTotalMs"]) != 2000 ||
		contracts.AnyIntNode(runTiming["firstTokenLatencyCount"]) != 1 ||
		contracts.AnyIntNode(runTiming["generationDurationMs"]) != 2500 {
		t.Fatalf("expected proxy run cumulative timing, got %#v", run)
	}
	if _, ok := runTiming["firstTokenLatencyMs"]; ok {
		t.Fatalf("did not expect proxy run average first token latency, got %#v", run)
	}
	if _, ok := runTiming["outputTokensPerSecond"]; ok {
		t.Fatalf("did not expect proxy run output speed in timing, got %#v", run)
	}
	chatUsage, _ := usage["chat"].(map[string]any)
	chatCost, _ := chatUsage["estimatedCost"].(map[string]any)
	if contracts.AnyIntNode(chatUsage["totalTokens"]) != 2_000_015 || FloatValue(chatCost["total"]) != 8.405 {
		t.Fatalf("expected proxy chat usage to include base tokens and run cost, got %#v", chatUsage)
	}
	chatTiming, _ := chatUsage["timing"].(map[string]any)
	if contracts.AnyIntNode(chatTiming["firstTokenLatencyTotalMs"]) != 2500 ||
		contracts.AnyIntNode(chatTiming["firstTokenLatencyCount"]) != 2 ||
		contracts.AnyIntNode(chatTiming["generationDurationMs"]) != 3000 {
		t.Fatalf("expected proxy chat cumulative timing, got %#v", chatUsage)
	}
	if _, ok := chatTiming["firstTokenLatencyMs"]; ok {
		t.Fatalf("did not expect proxy chat average first token latency, got %#v", chatUsage)
	}
	if _, ok := chatTiming["outputTokensPerSecond"]; ok {
		t.Fatalf("did not expect proxy chat output speed in timing, got %#v", chatUsage)
	}
	if runUsage.EstimatedCostCurrency != "CNY" || runUsage.EstimatedCostTotal != 8.405 {
		t.Fatalf("expected proxy run usage to capture cost, got %#v", runUsage)
	}
}
