package runexec

import (
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func TestProxyEventRecorderPersistsDecoratedUsageSnapshotCost(t *testing.T) {
	const startedAtMillis int64 = 1_700_000_000_000
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-proxy-cost", "proxy-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.OnRunStarted(chat.RunStart{
		ChatID: "chat-proxy-cost", RunID: "run-proxy-cost", StartedAtMillis: startedAtMillis,
	}); err != nil {
		t.Fatalf("record run start: %v", err)
	}
	stepWriter := chat.NewStepWriter(store, "chat-proxy-cost", "run-proxy-cost", "PROXY")
	recorder := NewProxyEventRecorder(
		runtimetypes.QueryCommand{ChatID: "chat-proxy-cost", RunID: "run-proxy-cost", AgentKey: "proxy-agent", Message: "hello"},
		startedAtMillis,
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
		Timestamp: startedAtMillis + 1,
		Payload:   map[string]any{"contentId": "content-1", "runId": "run-proxy-cost"},
	})
	recorder.OnEvent(stream.EventData{
		Type:      "content.delta",
		Timestamp: startedAtMillis + 2,
		Payload:   map[string]any{"contentId": "content-1", "delta": "answer"},
	})
	recorder.OnEvent(stream.EventData{
		Type:      "content.end",
		Timestamp: startedAtMillis + 3,
		Payload:   map[string]any{"contentId": "content-1"},
	})
	usageEvent := stream.EventData{
		Type:      "usage.snapshot",
		Timestamp: startedAtMillis + 4,
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
		Timestamp: startedAtMillis + 5,
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
