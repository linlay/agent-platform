package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/querymessages"
	"context"
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
		fixture.server.ServeHTTP(response, httptest.NewRequest("POST", "/api/query?detail=full", strings.NewReader(`{"agentKey":"mock-agent","chatId":"live-wait-chat","runId":"live-wait-run","message":"wait"}`)))
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

type runEnvWaitCheckpointSink struct {
	checkpoints chan *contracts.WaitCheckpoint
}

func (s runEnvWaitCheckpointSink) EmitToolOutput(context.Context, contracts.ToolOutput) error {
	return nil
}
func (s runEnvWaitCheckpointSink) EmitToolWait(_ context.Context, w contracts.ToolWait) error {
	s.checkpoints <- w.Checkpoint
	return nil
}
