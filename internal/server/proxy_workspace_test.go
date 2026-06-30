package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"

	gws "github.com/gorilla/websocket"
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
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
				"  timeout: 30",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","accessLevel":"default","params":{"channel":"desktop"}}`)
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
	if payload["accessLevel"] != "default" {
		t.Fatalf("expected upstream accessLevel default, got %#v", payload)
	}
	if params["channel"] != "desktop" || params["cwd"] != filepath.Clean(workspace) {
		t.Fatalf("unexpected upstream params %#v", params)
	}
}

func TestProxyQueryNonStreamAggregatesSSEUpstream(t *testing.T) {
	workspace := t.TempDir()
	captured := make(chan map[string]any, 1)
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		captured <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"content.start","contentId":"content-1","runId":"upstream-run"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","contentId":"content-1","delta":"proxy ","runId":"upstream-run"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","contentId":"content-1","delta":"answer","runId":"upstream-run"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.end","contentId":"content-1","text":"proxy answer","runId":"upstream-run"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run","usage":{"run":{"promptTokens":4,"completionTokens":2,"totalTokens":6}}}`+"\n\n")
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
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
				"  timeout: 30",
			})
		},
	})

	rec := httptest.NewRecorder()
	chatID := "chat-proxy-nonstream"
	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","agentKey":"mock-agent","message":"proxy me","stream":false,"includeUsage":true,"params":{"channel":"desktop"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected json content type, got %q", got)
	}
	if strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("did not expect sse body, got %s", rec.Body.String())
	}
	var queryResp api.ApiResponse[api.QueryResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &queryResp); err != nil {
		t.Fatalf("decode query response: %v", err)
	}
	if queryResp.Data.Content != "proxy answer" {
		t.Fatalf("unexpected query response %#v", queryResp)
	}
	if queryResp.Data.Usage == nil || queryResp.Data.Usage.TotalTokens != 6 {
		t.Fatalf("expected proxy usage in response, got %#v", queryResp.Data.Usage)
	}

	var payload map[string]any
	select {
	case payload = <-captured:
	default:
		t.Fatalf("expected upstream payload")
	}
	if payload["stream"] != true {
		t.Fatalf("expected platform to request upstream streaming, got %#v", payload)
	}

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertPersistedEventTypes(t, chatResp.Data.Events,
		"request.query",
		"chat.start",
		"run.start",
		"content.snapshot",
		"run.complete",
	)
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
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(t.TempDir()),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
				"  timeout: 30",
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

func TestProxyQueryDefaultsToWebSocketAndForwardsRuntimeWorkspaceRootAsCWD(t *testing.T) {
	workspace := t.TempDir()
	captured := make(chan map[string]any, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"models": []map[string]any{{
						"key":     "gpt-5-codex",
						"name":    "GPT-5 Codex",
						"modelId": "gpt-5-codex",
					}},
				},
			}); err != nil {
				t.Fatalf("encode model list: %v", err)
			}
			return
		case "/ws":
		default:
			t.Fatalf("expected /api/models or /ws, got %s", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade upstream websocket: %v", err)
		}
		defer conn.Close()
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatalf("read upstream websocket frame: %v", err)
		}
		captured <- frame
		if err := conn.WriteJSON(map[string]any{
			"event": map[string]any{
				"type":  "run.complete",
				"runId": "upstream-run",
			},
		}); err != nil {
			t.Fatalf("write upstream websocket completion: %v", err)
		}
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
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","params":{"channel":"desktop"}}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var frame map[string]any
	select {
	case frame = <-captured:
	default:
		t.Fatalf("expected upstream websocket frame")
	}
	if frame["type"] != "request.query" {
		t.Fatalf("expected request.query websocket frame, got %#v", frame)
	}
	inner, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", frame["payload"])
	}
	params, ok := inner["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params object, got %#v", inner["params"])
	}
	if params["channel"] != "desktop" || params["cwd"] != filepath.Clean(workspace) {
		t.Fatalf("unexpected upstream websocket params %#v", params)
	}
}

func TestACPCoderQueryUsesGlobalProxyAndForwardsWorkspaceAndModel(t *testing.T) {
	workspace := t.TempDir()
	writeTestGitHead(t, workspace, "main")
	captured := make(chan map[string]any, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"models": []map[string]any{{
						"key":     "gpt-5-codex",
						"name":    "GPT-5 Codex",
						"modelId": "gpt-5-codex",
					}},
				},
			}); err != nil {
				t.Fatalf("encode model list: %v", err)
			}
			return
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("upgrade upstream websocket: %v", err)
			}
			defer conn.Close()
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read upstream websocket frame: %v", err)
			}
			captured <- frame
			if err := conn.WriteJSON(map[string]any{
				"event": map[string]any{
					"type":  "run.complete",
					"runId": "upstream-run",
				},
			}); err != nil {
				t.Fatalf("write upstream websocket completion: %v", err)
			}
		default:
			t.Fatalf("expected /api/models or /ws, got %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"codex": {BaseURL: upstream.URL, AuthToken: "coder-token", Timeout: 420},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock ACP Coder",
				"role: 测试代理",
				"description: acp coder test agent",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  acpProxyId: codex",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"projectConfig:",
				"  git:",
				"    expectedBranch: main",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","params":{"channel":"desktop"}}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var frame map[string]any
	select {
	case frame = <-captured:
	default:
		t.Fatalf("expected upstream websocket frame")
	}
	inner, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", frame["payload"])
	}
	if inner["agentKey"] != "mock-agent" {
		t.Fatalf("expected platform agent key, got %#v", inner["agentKey"])
	}
	params, ok := inner["params"].(map[string]any)
	if !ok || params["cwd"] != filepath.Clean(workspace) || params["channel"] != "desktop" {
		t.Fatalf("unexpected upstream params %#v", inner["params"])
	}
	model, ok := inner["model"].(map[string]any)
	if !ok || model["key"] != "mock-model" || model["modelId"] != "mock-model-id" {
		t.Fatalf("unexpected upstream model %#v", inner["model"])
	}
}

func TestACPCoderForwardsProviderlessModel(t *testing.T) {
	workspace := t.TempDir()
	writeTestGitHead(t, workspace, "main")
	captured := make(chan map[string]any, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"models": []map[string]any{{
						"key":     "gpt-5-codex",
						"name":    "GPT-5 Codex",
						"modelId": "gpt-5-codex",
					}},
				},
			}); err != nil {
				t.Fatalf("encode model list: %v", err)
			}
			return
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("upgrade upstream websocket: %v", err)
			}
			defer conn.Close()
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read upstream websocket frame: %v", err)
			}
			captured <- frame
			if err := conn.WriteJSON(map[string]any{
				"event": map[string]any{
					"type":  "run.complete",
					"runId": "upstream-run",
				},
			}); err != nil {
				t.Fatalf("write upstream websocket completion: %v", err)
			}
		default:
			t.Fatalf("expected /api/models or /ws, got %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"codex": {BaseURL: upstream.URL},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			modelPath := filepath.Join(cfg.Paths.RegistriesDir, "models", "gpt-5-codex.yml")
			if err := os.WriteFile(modelPath, []byte(strings.Join([]string{
				"key: gpt-5-codex",
				"name: GPT-5 Codex",
				"modelId: gpt-5-codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write providerless model config: %v", err)
			}
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock ACP Coder",
				"role: 测试代理",
				"description: acp coder test agent",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: gpt-5-codex",
				"runtimeConfig:",
				"  acpProxyId: codex",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"projectConfig:",
				"  git:",
				"    expectedBranch: main",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","model":{"key":"gpt-5-codex"}}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var frame map[string]any
	select {
	case frame = <-captured:
	default:
		t.Fatalf("expected upstream websocket frame")
	}
	inner, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", frame["payload"])
	}
	model, ok := inner["model"].(map[string]any)
	if !ok || model["key"] != "gpt-5-codex" || model["modelId"] != "gpt-5-codex" {
		t.Fatalf("unexpected upstream model %#v", inner["model"])
	}
}

func TestACPCoderRejectsRequestCWDParam(t *testing.T) {
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
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"codex": {BaseURL: upstream.URL},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"mode: CODER",
				"runtimeConfig:",
				"  acpProxyId: codex",
				"  workspaceRoot: " + filepath.ToSlash(t.TempDir()),
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","params":{"cwd":"/tmp/other"}}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if upstreamHit.Load() {
		t.Fatalf("did not expect upstream request when params.cwd is rejected")
	}
}

func TestACPCoderForwardsPlanningMode(t *testing.T) {
	workspace := t.TempDir()
	writeTestGitHead(t, workspace, "main")
	captured := make(chan map[string]any, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"models": []map[string]any{{
						"key":     "gpt-5-codex",
						"name":    "GPT-5 Codex",
						"modelId": "gpt-5-codex",
					}},
				},
			}); err != nil {
				t.Fatalf("encode model list: %v", err)
			}
			return
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("upgrade upstream websocket: %v", err)
			}
			defer conn.Close()
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read upstream websocket frame: %v", err)
			}
			captured <- frame
			if err := conn.WriteJSON(map[string]any{
				"event": map[string]any{
					"type":  "run.complete",
					"runId": "upstream-run",
				},
			}); err != nil {
				t.Fatalf("write upstream websocket completion: %v", err)
			}
		default:
			t.Fatalf("expected /api/models or /ws, got %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"codex": {BaseURL: upstream.URL},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: gpt-5-codex",
				"runtimeConfig:",
				"  acpProxyId: codex",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
				"projectConfig:",
				"  git:",
				"    expectedBranch: main",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"plan","planningMode":true}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var frame map[string]any
	select {
	case frame = <-captured:
	default:
		t.Fatalf("expected upstream websocket frame")
	}
	inner, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", frame["payload"])
	}
	if inner["planningMode"] != true {
		t.Fatalf("expected planningMode=true forwarded, got %#v", inner)
	}
}

func TestACPCoderRejectsUnknownProxyID(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"other": {BaseURL: "http://127.0.0.1:3211"},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"mode: CODER",
				"runtimeConfig:",
				"  acpProxyId: codex",
				"  workspaceRoot: " + filepath.ToSlash(t.TempDir()),
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy"}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ACP proxy") {
		t.Fatalf("expected ACP proxy config error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProxyQueryPayloadWithWorkspaceAddsCWDForWebSocket(t *testing.T) {
	req := api.QueryRequest{
		RequestID:   "req-1",
		RunID:       "run-1",
		ChatID:      "chat-1",
		AgentKey:    "proxy-agent",
		Message:     "hello",
		AccessLevel: "default",
		PlanningMode: func() *bool {
			v := false
			return &v
		}(),
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
	if inner["accessLevel"] != "default" {
		t.Fatalf("expected websocket accessLevel default, got %#v", inner["accessLevel"])
	}
	if inner["planningMode"] != false {
		t.Fatalf("expected explicit planningMode=false, got %#v", inner["planningMode"])
	}
	if params["channel"] != "desktop" || params["cwd"] != "/workspace/project" {
		t.Fatalf("unexpected websocket params %#v", params)
	}
}

func writeTestGitHead(t *testing.T, workspace string, branch string) {
	t.Helper()
	gitDir := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		t.Fatalf("write git head: %v", err)
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
