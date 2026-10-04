package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

func TestProxyUsageTrackerDecoratesUsageSnapshotWithEstimatedCost(t *testing.T) {
	runUsage := chat.UsageData{}
	tracker := newProxyUsageTracker(
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
	if current["modelKey"] != "mock-model" || floatValue(currentCost["total"]) != 8.405 {
		t.Fatalf("expected proxy current cost decoration, got %#v", current)
	}
	run, _ := usage["run"].(map[string]any)
	runCost, _ := run["estimatedCost"].(map[string]any)
	if floatValue(runCost["total"]) != 8.405 {
		t.Fatalf("expected proxy run cost from current usage, got %#v", run)
	}
	runTiming, _ := run["timing"].(map[string]any)
	if AnyIntNode(runTiming["firstTokenLatencyTotalMs"]) != 2000 ||
		AnyIntNode(runTiming["firstTokenLatencyCount"]) != 1 ||
		AnyIntNode(runTiming["generationDurationMs"]) != 2500 {
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
	if AnyIntNode(chatUsage["totalTokens"]) != 2_000_015 || floatValue(chatCost["total"]) != 8.405 {
		t.Fatalf("expected proxy chat usage to include base tokens and run cost, got %#v", chatUsage)
	}
	chatTiming, _ := chatUsage["timing"].(map[string]any)
	if AnyIntNode(chatTiming["firstTokenLatencyTotalMs"]) != 2500 ||
		AnyIntNode(chatTiming["firstTokenLatencyCount"]) != 2 ||
		AnyIntNode(chatTiming["generationDurationMs"]) != 3000 {
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

func TestProxyEventRecorderPersistsDecoratedUsageSnapshotCost(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-proxy-cost", "proxy-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startServerFixtureRun(t, store, "chat-proxy-cost", "run-proxy-cost", testEpochMillis)
	stepWriter := chat.NewStepWriter(store, "chat-proxy-cost", "run-proxy-cost", "PROXY")
	recorder := newProxyEventRecorder(
		api.QueryRequest{ChatID: "chat-proxy-cost", RunID: "run-proxy-cost", AgentKey: "proxy-agent", Message: "hello"},
		1_700_000_000_000,
		catalog.AgentDefinition{Key: "proxy-agent", Mode: "PROXY"},
		store,
		stepWriter,
		nil,
		nil,
		chat.UsageData{},
		writeUsageCostRegistry(t),
		config.BillingConfig{Currency: "CNY"},
	)
	recorder.OnEvent(stream.EventData{
		Type:      "content.start",
		Timestamp: testEpochMillis + 1,
		Payload:   map[string]any{"contentId": "content-1", "runId": "run-proxy-cost"},
	})
	recorder.OnEvent(stream.EventData{
		Type:      "content.delta",
		Timestamp: testEpochMillis + 2,
		Payload:   map[string]any{"contentId": "content-1", "delta": "answer"},
	})
	recorder.OnEvent(stream.EventData{
		Type:      "content.end",
		Timestamp: testEpochMillis + 3,
		Payload:   map[string]any{"contentId": "content-1"},
	})
	usageEvent := stream.EventData{
		Type:      "usage.snapshot",
		Timestamp: testEpochMillis + 4,
		Payload: map[string]any{
			"usage": map[string]any{
				"current": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
					"modelKey":         "mock-model",
				},
				"run": map[string]any{
					"promptTokens":     1_000_000,
					"completionTokens": 1_000_000,
					"totalTokens":      2_000_000,
				},
			},
		},
	}
	recorder.DecorateEvent(&usageEvent)
	recorder.OnEvent(usageEvent)
	terminalEvent := stream.EventData{
		Type:      "run.complete",
		Timestamp: testEpochMillis + 5,
		Payload:   map[string]any{"runId": "run-proxy-cost"},
	}
	recorder.DecorateEvent(&terminalEvent)
	recorder.OnEvent(terminalEvent)

	persisted, completion := recorder.Finish()
	if !persisted {
		t.Fatalf("expected proxy completion to persist")
	}
	if completion.Usage.EstimatedCostCurrency != "CNY" || completion.Usage.EstimatedCostTotal != 9 {
		t.Fatalf("expected completion usage cost from decorated snapshot, got %#v", completion.Usage)
	}
	detail, err := store.LoadChat("chat-proxy-cost")
	if err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if detail.ReplayUsage.LastRun.EstimatedCostCurrency != "CNY" || detail.ReplayUsage.LastRun.EstimatedCostTotal != 9 {
		t.Fatalf("expected replay lastRun cost from proxy step usage, got %#v", detail.ReplayUsage.LastRun)
	}
	if detail.ReplayUsage.Chat.EstimatedCostCurrency != "CNY" || detail.ReplayUsage.Chat.EstimatedCostTotal != 9 {
		t.Fatalf("expected replay chat cost from proxy step usage, got %#v", detail.ReplayUsage.Chat)
	}
}

func writeUsageCostRegistry(t *testing.T) *models.ModelRegistry {
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
	if err := os.WriteFile(filepath.Join(root, "models", "expensive.yml"), []byte(strings.Join([]string{
		"key: expensive-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: expensive-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.00",
		"  inputCacheMiss: 10.00",
		"  output: 20.00",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write expensive model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "no-pricing.yml"), []byte(strings.Join([]string{
		"key: no-pricing-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: no-pricing-model-id",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write no-pricing model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}
