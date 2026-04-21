package server

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/runctl"
)

func TestHTTPQueryStreamClosesDuringRootContextShutdown(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		if _, err := io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"partial"}}]}`+"\n\n"); err != nil {
			t.Fatalf("write partial delta: %v", err)
		}
		flusher.Flush()
		<-r.Context().Done()
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-runner", "agent.yml"), []string{
				"key: mock-runner",
				"name: Mock Runner",
				"role: 测试代理",
				"description: test agent",
				"modelConfig:",
				"  modelKey: mock-model",
				"mode: REACT",
				"react:",
				"  maxSteps: 6",
			})
		},
	})

	httpServer := newLoopbackServerWithBaseContext(t, fixture.server, rootCtx)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"shutdown me"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	waitForSSEPayloadLine(t, reader, `"type":"content.delta"`)

	shutdownDone := make(chan error, 1)
	go func() {
		cancelRoot()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- httpServer.server.Shutdown(ctx)
	}()

	waitForBodyClose(t, resp.Body, time.Second)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("server shutdown timed out")
	}
}

func TestHTTPRunStreamDetachesObserverDuringRootContextShutdown(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	fixture := newTestFixture(t)
	runs := fixture.runs.(*runctl.InMemoryRunManager)
	runID := "run_http_shutdown"
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID:    runID,
		ChatID:   "chat_http_shutdown",
		AgentKey: "mock-runner",
	})

	httpServer := newLoopbackServerWithBaseContext(t, fixture.server, rootCtx)
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/attach?runId=" + runID)
	if err != nil {
		t.Fatalf("get run stream: %v", err)
	}
	defer resp.Body.Close()

	waitForObserverCount(t, runs, runID, 1, time.Second)

	shutdownDone := make(chan error, 1)
	go func() {
		cancelRoot()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- httpServer.server.Shutdown(ctx)
	}()

	waitForBodyClose(t, resp.Body, time.Second)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("server shutdown timed out")
	}

	waitForObserverCount(t, runs, runID, 0, time.Second)
}

func TestProxyQueryCancelsUpstreamRequestDuringRootContextShutdown(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	upstreamCanceled := make(chan struct{}, 1)
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected flusher")
		}
		if _, err := io.WriteString(w, "data: "+`{"type":"content.delta","timestamp":1,"payload":{"contentId":"content-1","delta":"hello","runId":"run-proxy","chatId":"chat-proxy"}}`+"\n\n"); err != nil {
			t.Fatalf("write upstream event: %v", err)
		}
		flusher.Flush()
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-runner", "agent.yml")
			writeAgentConfig(t, agentPath, []string{
				"key: mock-runner",
				"name: Mock Proxy Runner",
				"role: 测试代理",
				"description: proxy test agent",
				"mode: PROXY",
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  timeoutMs: 30000",
			})
		},
	})

	httpServer := newLoopbackServerWithBaseContext(t, fixture.server, rootCtx)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"proxy me"}`))
	if err != nil {
		t.Fatalf("post proxy query: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	waitForSSEPayloadLine(t, reader, `"type":"content.delta"`)

	shutdownDone := make(chan error, 1)
	go func() {
		cancelRoot()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- httpServer.server.Shutdown(ctx)
	}()

	waitForBodyClose(t, resp.Body, time.Second)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("server shutdown timed out")
	}

	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatalf("expected upstream proxy request to be canceled")
	}
}

func newLoopbackServerWithBaseContext(t *testing.T, handler http.Handler, baseCtx context.Context) *loopbackServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback server: %v", err)
	}
	server := &http.Server{
		Handler:     handler,
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
	go func() {
		_ = server.Serve(listener)
	}()
	result := &loopbackServer{
		URL:    "http://" + listener.Addr().String(),
		server: server,
		ln:     listener,
	}
	t.Cleanup(result.Close)
	return result
}

func waitForSSEPayloadLine(t *testing.T, reader *bufio.Reader, needle string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		line, err := reader.ReadString('\n')
		if strings.Contains(line, needle) {
			return
		}
		if err != nil {
			t.Fatalf("read sse line before %s: %v", needle, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", needle)
		}
	}
}

func waitForBodyClose(t *testing.T, body io.ReadCloser, timeout time.Duration) {
	t.Helper()
	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, body)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}
	case <-time.After(timeout):
		t.Fatalf("response body did not close within %s", timeout)
	}
}

func waitForObserverCount(t *testing.T, runs *runctl.InMemoryRunManager, runID string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, ok := runs.RunStatus(runID)
		if ok && status.ObserverCount == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, ok := runs.RunStatus(runID)
	if !ok {
		t.Fatalf("run %s not found while waiting for observer count %d", runID, want)
	}
	t.Fatalf("expected observer count %d, got %d", want, status.ObserverCount)
}

func writeAgentConfig(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
}
