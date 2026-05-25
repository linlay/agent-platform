package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func TestProxyQueryForwardsRuntimeWorkspaceRootAsCWD(t *testing.T) {
	workspace := t.TempDir()
	captured := make(chan map[string]any, 1)
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		captured <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run"}`+"\n\n")
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"role: 测试代理",
				"description: proxy test agent",
				"mode: PROXY",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  timeoutMs: 30000",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","params":{"channel":"desktop"}}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	select {
	case payload = <-captured:
	default:
		t.Fatalf("expected upstream payload")
	}
	params, ok := payload["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params object, got %#v", payload["params"])
	}
	if params["channel"] != "desktop" || params["cwd"] != filepath.Clean(workspace) {
		t.Fatalf("unexpected upstream params %#v", params)
	}
}

func TestProxyQueryRejectsRequestCWDParam(t *testing.T) {
	var upstreamHit atomic.Bool
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run"}`+"\n\n")
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"role: 测试代理",
				"description: proxy test agent",
				"mode: PROXY",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(t.TempDir()),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  timeoutMs: 30000",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","params":{"cwd":"/tmp/other","channel":"desktop"}}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runtimeConfig.workspaceRoot") {
		t.Fatalf("expected workspaceRoot guidance, got %s", rec.Body.String())
	}
	if upstreamHit.Load() {
		t.Fatalf("did not expect upstream request when params.cwd is rejected")
	}
}

func TestProxyQueryPayloadWithWorkspaceAddsCWDForWebSocket(t *testing.T) {
	req := api.QueryRequest{
		RequestID: "req-1",
		RunID:     "run-1",
		ChatID:    "chat-1",
		AgentKey:  "proxy-agent",
		Message:   "hello",
		Params: map[string]any{
			"channel": "desktop",
		},
	}
	payload := proxyQueryPayloadWithWorkspace(req, &catalog.ProxyConfig{}, nil, "/workspace/project")
	inner, ok := payload["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", payload)
	}
	params, ok := inner["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params object, got %#v", inner["params"])
	}
	if params["channel"] != "desktop" || params["cwd"] != "/workspace/project" {
		t.Fatalf("unexpected websocket params %#v", params)
	}
}

func TestProxyForwardParamsWithoutWorkspaceKeepsLegacyParams(t *testing.T) {
	params := proxyForwardParams(api.QueryRequest{Params: map[string]any{"channel": "desktop"}}, "")
	if params["channel"] != "desktop" {
		t.Fatalf("expected existing param to be preserved, got %#v", params)
	}
	if _, ok := params["cwd"]; ok {
		t.Fatalf("did not expect cwd without runtime workspace root, got %#v", params)
	}
}
