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
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/timecontract"

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
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run","timestamp":1700000000000}`+"\n\n")
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

func TestProxyQueryPassesThroughUnknownModelOptions(t *testing.T) {
	captured := make(chan map[string]any, 1)
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		captured <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run","timestamp":1700000000000}`+"\n\n")
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
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
				"  timeout: 30",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me","model":{"key":"upstream-only","modelId":"upstream/model","reasoningEffort":"FASTEST","serviceTier":"burst"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/query", body)
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	select {
	case payload = <-captured:
	default:
		t.Fatalf("expected upstream payload")
	}
	model, ok := payload["model"].(map[string]any)
	if !ok {
		t.Fatalf("expected model object, got %#v", payload["model"])
	}
	if model["key"] != "upstream-only" ||
		model["modelId"] != "upstream/model" ||
		model["reasoningEffort"] != "FASTEST" ||
		model["serviceTier"] != "burst" {
		t.Fatalf("unexpected upstream model payload %#v", model)
	}
}

func TestProxyQueryRejectsInvalidUpstreamTimestampBeforeStreamStarts(t *testing.T) {
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run","timestamp":"1700000000000"}`+"\n\n")
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"mode: PROXY",
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
			})
		},
	})

	for name, body := range map[string]string{
		"stream":     `{"agentKey":"mock-agent","message":"proxy me"}`,
		"non-stream": `{"agentKey":"mock-agent","message":"proxy me","stream":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(body)))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
			}
			var response api.ApiResponse[map[string]any]
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			errorData, _ := response.Data["error"].(map[string]any)
			if response.Msg != "time contract violation" || errorData["code"] != "time_contract_violation" ||
				errorData["field"] != "timestamp" || errorData["location"] != "proxy.sse.event" ||
				errorData["expected"] != "epoch_ms_int64" {
				t.Fatalf("unexpected time contract response %#v", response)
			}
		})
	}
}

func TestProxyQueryEmitsLocalRunErrorForInvalidTimestampAfterStreamStarts(t *testing.T) {
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"seq":4,"type":"content.delta","contentId":"content-1","delta":"first","timestamp":1700000000000}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"seq":5,"type":"run.complete","timestamp":"1700000000001"}`+"\n\n")
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"mode: PROXY",
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
			})
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected started SSE response to remain 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected direct proxy SSE to terminate with [DONE], got %s", body)
	}
	messages := decodeSSEMessages(t, body)
	var localError map[string]any
	for _, message := range messages {
		if message["type"] == "run.error" {
			localError = message
		}
		if message["timestamp"] == "1700000000001" {
			t.Fatalf("invalid upstream event was forwarded: %#v", message)
		}
	}
	if localError == nil {
		t.Fatalf("expected local run.error, got %#v", messages)
	}
	if timestamp, ok := localError["timestamp"].(float64); !ok || timestamp < 1_000_000_000_000 {
		t.Fatalf("expected local epoch-ms timestamp, got %#v", localError["timestamp"])
	}
	if seq, ok := localError["seq"].(float64); !ok || seq != 5 {
		t.Fatalf("expected local error to continue stream sequence at 5, got %#v", localError["seq"])
	}
	errorData, _ := localError["error"].(map[string]any)
	if localError["code"] != "time_contract_violation" || errorData["field"] != "timestamp" ||
		errorData["location"] != "proxy.sse.event" || errorData["expected"] != "epoch_ms_int64" {
		t.Fatalf("unexpected local time contract error %#v", localError)
	}
	chatID, _ := localError["chatId"].(string)
	runID, _ := localError["runId"].(string)
	runs, err := fixture.chats.ListRuns(chatID)
	if err != nil || len(runs) != 1 || runs[0].RunID != runID || runs[0].FinishReason != "error" {
		t.Fatalf("time-contract proxy termination must persist an error completion: runs=%#v err=%v", runs, err)
	}
}

func TestProxyQueryStreamingPersistsCapturedRunStartAndLifecyclePushTimes(t *testing.T) {
	notifications := &recordingNotificationSink{}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"seq":1,"type":"run.complete","runId":"upstream-run","timestamp":1700000000000}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Proxy Agent",
				"mode: PROXY",
				"modelConfig:",
				"  modelKey: mock-model",
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
			})
		},
	})
	const (
		chatID = "chat_proxy_direct_lifecycle"
		runID  = "run_proxy_direct_lifecycle"
	)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"`+chatID+`",
		"runId":"`+runID+`",
		"agentKey":"mock-agent",
		"message":"capture direct proxy lifecycle"
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected started SSE response, got %d: %s", rec.Code, rec.Body.String())
	}

	runs, err := fixture.chats.ListRuns(chatID)
	if err != nil {
		t.Fatalf("list persisted runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != runID {
		t.Fatalf("unexpected persisted runs %#v", runs)
	}
	if err := timecontract.ValidateEpochMillis(runs[0].StartedAt, "startedAt", "test"); err != nil {
		t.Fatalf("persisted proxy run start must be epoch-ms: %v", err)
	}
	if err := timecontract.ValidateEpochMillis(runs[0].CompletedAt, "completedAt", "test"); err != nil {
		t.Fatalf("persisted proxy run completion must be epoch-ms: %v", err)
	}
	var startedAt, finishedAt int64
	startedIndex, finishedIndex := -1, -1
	for index, eventType := range notifications.EventTypes() {
		payload := notifications.Payloads()[index]
		switch eventType {
		case "run.started":
			startedIndex = index
			startedAt, _ = payload["startedAt"].(int64)
		case "run.finished":
			finishedIndex = index
			finishedAt, _ = payload["finishedAt"].(int64)
		}
	}
	if startedIndex < 0 || finishedIndex < 0 || startedIndex >= finishedIndex {
		t.Fatalf("expected ordered direct-proxy run lifecycle pushes, got %#v", notifications.EventTypes())
	}
	if startedAt != runs[0].StartedAt || finishedAt != runs[0].CompletedAt {
		t.Fatalf("lifecycle push times must match persistence: started=%d/%d finished=%d/%d", startedAt, runs[0].StartedAt, finishedAt, runs[0].CompletedAt)
	}
}

func TestProxyAgentWithoutModelOmitsModelMetadata(t *testing.T) {
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run","timestamp":1700000000000}`+"\n\n")
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
				"proxyConfig:",
				"  baseUrl: " + upstream.URL,
				"  transport: sse",
			})
		},
	})

	detailRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(detailRec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=mock-agent", nil))
	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d: %s", detailRec.Code, detailRec.Body.String())
	}
	var detailRaw map[string]any
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detailRaw); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	detailData, ok := detailRaw["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected detail data object, got %#v", detailRaw["data"])
	}
	if _, exists := detailData["model"]; exists {
		t.Fatalf("PROXY detail without model key should omit model, got %#v", detailData)
	}
	detailMeta, ok := detailData["meta"].(map[string]any)
	if !ok {
		t.Fatalf("expected detail meta object, got %#v", detailData["meta"])
	}
	if _, exists := detailMeta["modelKey"]; exists {
		t.Fatalf("PROXY detail without model key should omit modelKey meta, got %#v", detailMeta)
	}
	if _, exists := detailMeta["modelKeys"]; exists {
		t.Fatalf("PROXY detail without model key should omit modelKeys meta, got %#v", detailMeta)
	}

	summaryRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(summaryRec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=nav", nil))
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("expected summaries 200, got %d: %s", summaryRec.Code, summaryRec.Body.String())
	}
	var summaries api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(summaryRec.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode summaries response: %v", err)
	}
	if len(summaries.Data) != 1 || summaries.Data[0].Key != "mock-agent" {
		t.Fatalf("expected mock-agent summary, got %#v", summaries.Data)
	}
	if _, exists := summaries.Data[0].Meta["model"]; exists {
		t.Fatalf("PROXY summary without model key should omit model meta, got %#v", summaries.Data[0].Meta)
	}
	if _, exists := summaries.Data[0].Meta["modelKey"]; exists {
		t.Fatalf("PROXY summary without model key should omit modelKey meta, got %#v", summaries.Data[0].Meta)
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
		_, _ = io.WriteString(w, `data: {"type":"content.start","contentId":"content-1","runId":"upstream-run","timestamp":1700000000000}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","contentId":"content-1","delta":"proxy ","runId":"upstream-run","timestamp":1700000000001}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","contentId":"content-1","delta":"answer","runId":"upstream-run","timestamp":1700000000002}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.end","contentId":"content-1","text":"proxy answer","runId":"upstream-run","timestamp":1700000000003}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"upstream-run","usage":{"run":{"promptTokens":4,"completionTokens":2,"totalTokens":6}},"timestamp":1700000000004}`+"\n\n")
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
				"type":      "run.complete",
				"runId":     "upstream-run",
				"timestamp": int64(1_700_000_000_000),
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

func TestChannelImportQueryUsesPlatformWSFrameAndRemoteAgentKey(t *testing.T) {
	captured := make(chan map[string]any, 1)
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/channel" {
			t.Fatalf("expected /ws/channel, got %s", r.URL.Path)
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
				"type":      "run.complete",
				"runId":     "remote-run",
				"timestamp": int64(1_700_000_000_000),
			},
		}); err != nil {
			t.Fatalf("write upstream websocket completion: %v", err)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Channels = []config.ChannelConfig{{
				ID:        "peer-a",
				Mode:      config.ChannelModeClient,
				Transport: config.ChannelTransportWebSocket,
				Protocol:  config.ChannelProtocolPlatformWS,
				Endpoint: config.ChannelEndpointConfig{
					URL: "ws" + strings.TrimPrefix(upstream.URL, "http") + "/ws/channel",
				},
			}}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"name: Mock Channel Agent",
				"mode: CHANNEL",
				"channelConfig:",
				"  channelId: peer-a",
				"  remoteAgentKey: coder",
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy me"}`)
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
	if frame["frame"] != "request" || frame["type"] != "/api/query" {
		t.Fatalf("expected platform-ws /api/query frame, got %#v", frame)
	}
	inner, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected payload object, got %#v", frame["payload"])
	}
	if inner["agentKey"] != "coder" {
		t.Fatalf("expected remote agent key coder, got %#v", inner["agentKey"])
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
			cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{
				"codex": {BaseURL: upstream.URL, AuthToken: "coder-token", TimeoutMS: 420000},
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
				"  acpBridgeId: codex",
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
			cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{
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
				"  acpBridgeId: codex",
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
			cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{
				"codex": {BaseURL: upstream.URL},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"mode: CODER",
				"runtimeConfig:",
				"  acpBridgeId: codex",
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
			cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{
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
				"  acpBridgeId: codex",
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
			cfg.CoderSettings.ACPBridges = map[string]config.CoderACPBridgeConfig{
				"other": {BaseURL: "http://127.0.0.1:3211"},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeAgentConfig(t, filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"), []string{
				"key: mock-agent",
				"mode: CODER",
				"runtimeConfig:",
				"  acpBridgeId: codex",
				"  workspaceRoot: " + filepath.ToSlash(t.TempDir()),
			})
		},
	})

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"agentKey":"mock-agent","message":"proxy"}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", body))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ACP bridge") {
		t.Fatalf("expected ACP bridge config error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProxyRequestTimeoutUsesACPBridgeMilliseconds(t *testing.T) {
	if got := proxyRequestTimeout(&catalog.ProxyConfig{Timeout: 1, TimeoutMS: 250}); got != 250*time.Millisecond {
		t.Fatalf("ACP bridge timeout = %s, want 250ms", got)
	}
	if got := proxyRequestTimeout(&catalog.ProxyConfig{Timeout: 2}); got != 2*time.Second {
		t.Fatalf("ordinary proxy timeout = %s, want 2s", got)
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
