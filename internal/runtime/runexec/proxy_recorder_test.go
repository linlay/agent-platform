package runexec

import (
	"encoding/json"
	"reflect"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func TestProxyRecorderPreservesOriginalQuery(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const chatID, runID = "chat-proxy-original", "run-proxy-original"
	if _, _, err := store.EnsureChat(chatID, "proxy-agent", "hello"); err != nil {
		t.Fatal(err)
	}
	const raw = `{"message":"hello","stream":false,"includeUsage":false,"references":[],"model":null}`
	writer := chat.NewStepWriter(store, chatID, runID, "PROXY")
	editing := false
	NewProxyEventRecorder(runtimetypes.QueryCommand{
		OriginalQuery: []byte(raw), RequestID: runID, ChatID: chatID, RunID: runID, AgentKey: "proxy-agent", Role: "user", Message: "hello",
		AccessLevel: "full_access", EditingMode: &editing, Model: &runtimetypes.QueryModelOptions{Key: "resolved-model"},
		References: []runtimetypes.Reference{{Type: "selection", ID: "quote", Text: "resolved quote"}},
	}, 1_700_000_000_000, catalog.AgentDefinition{}, store, writer, nil, nil, chat.UsageData{}, nil, config.BillingConfig{})
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	line, err := store.LoadRunQuery(chatID, runID)
	if err != nil || line == nil {
		t.Fatalf("load proxy query: %#v, %v", line, err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(line.Query, want) || line.AgentKey != "proxy-agent" || line.RequestPayload()["agentKey"] != "proxy-agent" {
		t.Fatalf("proxy request and platform decisions mixed: %#v", line)
	}
	model, _ := line.Model.(map[string]any)
	if line.AccessLevel != "full_access" || line.EditingMode == nil || *line.EditingMode || model["key"] != "resolved-model" || line.References == nil {
		t.Fatalf("proxy platform decisions were not persisted: %#v", line)
	}
}

func TestProxyEventRecorderPersistsDecoratedUsageSnapshotCost(t *testing.T) {
	const startedAtMillis int64 = 1_700_000_000_000
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-proxy-cost", "proxy-agent", "hello"); err != nil {
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
