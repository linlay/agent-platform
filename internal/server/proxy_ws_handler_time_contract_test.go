package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"

	gws "github.com/gorilla/websocket"
)

func TestProxyWebSocketHTTPSSEObserverTerminatesInvalidEventWithLocalTimeContractError(t *testing.T) {
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstreamRelease := make(chan struct{})
	var upstreamReleaseOnce sync.Once
	releaseUpstream := func() {
		upstreamReleaseOnce.Do(func() { close(upstreamRelease) })
	}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Keep the upstream request alive while the test injects the malformed
		// observer event. This exercises the HTTP SSE proxy observer boundary,
		// not the earlier upstream JSON decoder.
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		<-upstreamRelease
	}))
	defer releaseUpstream()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"mode: PROXY",
				"modelConfig:",
				"  modelKey: mock-model",
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: ws",
			})
		},
	})
	runs := fixture.runs.(*contracts.InMemoryRunManager)
	const (
		runID  = "run_proxy_ws_sse_time_contract"
		chatID = "chat_proxy_ws_sse_time_contract"
	)
	published := make(chan bool, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if eventBus, ok := runs.EventBus(runID); ok {
				eventBus.Publish(stream.EventData{
					Seq:       31,
					Type:      "content.delta",
					Timestamp: 1_700_000_000_000,
					Payload: map[string]any{
						"runId":  runID,
						"chatId": chatID,
						"delta":  "valid first event",
					},
				})
				eventBus.Publish(stream.EventData{
					Seq:       32,
					Type:      "content.delta",
					Timestamp: 0,
					Payload: map[string]any{
						"runId":  runID,
						"chatId": chatID,
						"delta":  "must not reach client",
					},
				})
				published <- true
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		published <- false
	}()

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"`+chatID+`",
		"runId":"`+runID+`",
		"agentKey":"mock-agent",
		"message":"trigger malformed observer event"
	}`)))
	if ok := <-published; !ok {
		t.Fatal("timed out waiting for proxy run event bus")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected started SSE response, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected SSE done sentinel after time-contract violation, got %s", body)
	}
	if strings.Contains(body, `"delta":"must not reach client"`) {
		t.Fatalf("invalid observer event reached proxy SSE: %s", body)
	}

	var localError map[string]any
	for _, event := range decodeSSEMessages(t, body) {
		if event["type"] == "run.error" {
			localError = event
		}
	}
	if localError == nil {
		t.Fatalf("expected local run.error, got %s", body)
	}
	assertLocalTimeContractRunError(t, localError, "timestamp")
	if got, ok := localError["seq"].(float64); !ok || got != 32 {
		t.Fatalf("local error seq = %#v, want 32", localError["seq"])
	}

	status, ok := runs.RunStatus(runID)
	if !ok {
		t.Fatalf("expected run status after terminated proxy observer")
	}
	if status.State != contracts.RunLoopStateCancelled || status.CompletedAt == 0 {
		t.Fatalf("expected cancelled and finished proxy run after SSE violation, got %#v", status)
	}
	// Let the proxy goroutine observe its upstream close and persist its own
	// cancellation before fixture cleanup closes the chat database.
	releaseUpstream()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, active := fixture.server.lookupProxyRun(runID); !active {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("proxy route %s did not finish after local time-contract termination", runID)
}
