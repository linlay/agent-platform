package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/querymessages"
	"agent-platform/internal/runtime/runstate"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitRestartContinuesSameRun(t *testing.T) {
	var calls atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if err := validateAwaitingToolPairs(payload["messages"]); err != nil {
			t.Error(err)
		}
		calls.Add(1)
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"resumed wait"},"finish_reason":"stop"}]}`, "[DONE]")
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) { cfg.PresetTools = append(cfg.PresetTools, "wait") }})
	now := time.Now().UnixMilli()
	seedDeferredAwaitingPayload(t, fixture.chats, "wait-chat", "wait-run", "wait-call", "wait", 0, now, map[string]any{"startedAt": now, "deadlineAt": now + 500, "description": "test recovery", "match": "any", "conditions": []any{}, "waitCheckpoint": contracts.WaitCheckpoint{StartedAt: now, Budget: contracts.NormalizeBudget(contracts.Budget{})}})
	if err := fixture.chats.AppendQueryLine("wait-chat", chat.QueryLine{ChatID: "wait-chat", RunID: "wait-run", UpdatedAt: now, Type: "query", Query: map[string]any{"runId": "wait-run", "chatId": "wait-chat", "agentKey": "mock-agent", "role": "user", "message": "wait then continue"}}); err != nil {
		t.Fatal(err)
	}
	runs := runstate.NewManager()
	defer runs.Finish("wait-run")
	_, err := newRuntimeServer(deferredRestartDependencies(fixture, runs, fixture.chats, nil))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		status, ok := runs.RunStatus("wait-run")
		if ok && status.CompletedAt > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() != 1 {
		detail, _ := fixture.chats.LoadChat("wait-chat")
		t.Fatalf("model calls=%d detail=%+v", calls.Load(), detail)
	}
	summary, err := fixture.chats.Summary("wait-chat")
	if err != nil || summary.PendingAwaiting != nil {
		t.Fatalf("pending wait remains: %+v %v", summary, err)
	}
	status, ok := runs.RunStatus("wait-run")
	if !ok || status.StartedAt != now || status.CompletedAt == 0 {
		t.Fatalf("run identity changed: %+v", status)
	}
}

func TestLiveWaitBlankSteerContinuesSameRun(t *testing.T) {
	var calls atomic.Int32
	var resumed atomic.Value
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeProviderSSE(t, w, providerToolCallsFrame(t, []providerToolCallSpec{{ID: "native-wait", Name: "wait", Args: map[string]any{"offset": "1H", "description": "wait test"}}}), "[DONE]")
			return
		}
		body, _ := io.ReadAll(r.Body)
		resumed.Store(string(body))
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, "[DONE]")
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) { cfg.PresetTools = append(cfg.PresetTools, "wait") }})
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	wrapped := &awaitingBarrierStore{awaitingReconcileFailureStore: &awaitingReconcileFailureStore{Store: fixture.chats}, pending: func(item chat.PendingAwaiting) {
		if item.Mode == "wait" {
			close(ready)
			<-release
		}
	}}
	fixture.server.deps.Chats = wrapped
	bindTestRuntime(fixture.server)
	response := httptest.NewRecorder()
	go func() {
		defer close(done)
		fixture.server.ServeHTTP(response, httptest.NewRequest("POST", "/api/query", strings.NewReader(`{"agentKey":"mock-agent","chatId":"live-wait-chat","runId":"live-wait-run","message":"wait"}`)))
	}()
	select {
	case <-ready:
	case <-done:
		t.Fatalf("ended before wait: %s", response.Body.String())
	case <-time.After(8 * time.Second):
		t.Fatal("no wait checkpoint")
	}
	ask, err := fixture.chats.LoadAwaitingAsk("live-wait-chat", "native-wait")
	if err != nil || ask == nil || ask.Mode != "wait" {
		t.Fatalf("wait not persisted: %+v %v", ask, err)
	}
	steer := httptest.NewRecorder()
	fixture.server.ServeHTTP(steer, httptest.NewRequest("POST", "/api/steer", strings.NewReader(`{"agentKey":"mock-agent","chatId":"live-wait-chat","runId":"live-wait-run"}`)))
	if steer.Code != 200 || !strings.Contains(steer.Body.String(), `"accepted":true`) {
		t.Fatal(steer.Code, steer.Body.String())
	}
	unblock()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("steer did not continue")
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load(), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "steered") || !strings.Contains(response.Body.String(), `"continued":true`) {
		t.Fatal("blank steer did not end the wait as continued", response.Body.String())
	}
	if input, _ := resumed.Load().(string); !strings.Contains(input, querymessages.EmptySteerContinuation) {
		t.Fatal("model did not receive the continuation instruction", input)
	}
	if strings.Count(response.Body.String(), `"type":"request.steer"`) != 1 {
		t.Fatal("blank steer must be published once like any steer", response.Body.String())
	}
	if raw, err := os.ReadFile(filepath.Join(fixture.chats.ChatDir("live-wait-chat") + ".jsonl")); err != nil || strings.Count(string(raw), `"_type":"steer"`) != 1 {
		t.Fatal("blank steer not persisted as one steer line", err, string(raw))
	}
	late := httptest.NewRecorder()
	fixture.server.ServeHTTP(late, httptest.NewRequest("POST", "/api/steer", strings.NewReader(`{"agentKey":"mock-agent","chatId":"live-wait-chat","runId":"live-wait-run"}`)))
	if strings.Contains(late.Body.String(), `"accepted":true`) {
		t.Fatal("blank steer accepted by a finished Run", late.Body.String())
	}
	if strings.Count(response.Body.String(), `"type":"run.start"`) != 1 {
		t.Fatal("wait created another Run", response.Body.String())
	}
	summary, _ := fixture.chats.Summary("live-wait-chat")
	if summary.PendingAwaiting != nil {
		t.Fatal("wait still pending")
	}
	if strings.Contains(response.Body.String(), "waitCheckpoint") {
		t.Fatal("private checkpoint leaked")
	}
}
