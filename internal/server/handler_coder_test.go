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
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func writeAnthropicProviderSSE(t *testing.T, w http.ResponseWriter, frames ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatalf("expected flusher")
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, frame+"\n\n"); err != nil {
			t.Fatalf("write anthropic sse frame: %v", err)
		}
		flusher.Flush()
	}
}

func setupCoderRuntime(t *testing.T, cfg *config.Config) {
	t.Helper()
	modelPath := filepath.Join(cfg.Paths.RegistriesDir, "models", "coder-model.yml")
	if err := os.WriteFile(modelPath, []byte(strings.Join([]string{
		"key: coder-model",
		"name: Coder Model",
		"provider: mock",
		"protocol: ANTHROPIC",
		"modelId: coder-model-id",
		"isFunction: true",
		"isReasoner: true",
		"isVision: true",
		"maxInputTokens: 200000",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write coder model config: %v", err)
	}
	agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	if err := os.WriteFile(agentPath, []byte(strings.Join([]string{
		"key: mock-agent",
		"name: Mock Coder",
		"role: 测试代理",
		"description: coder test agent",
		"modelConfig:",
		"  modelKey: mock-model",
		"toolConfig:",
		"  tools:",
		"    - datetime",
		"runtimeConfig:",
		"  environmentId: shell",
		"  level: RUN",
		"mode: CODER",
		"stageSettings:",
		"  execute:",
		"    modelKey: mock-model",
		"    reasoningEffort: LOW",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write coder agent config: %v", err)
	}
}

func TestCoderModelOptionsHTTP(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			setupCoderRuntime(t, cfg)
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if response.Data.DefaultModelKey != "mock-model" || response.Data.DefaultReasoningEffort != "MEDIUM" {
		t.Fatalf("unexpected defaults %#v", response.Data)
	}
	if len(response.Data.ReasoningEfforts) != 4 ||
		response.Data.ReasoningEfforts[0].Key != "NONE" ||
		response.Data.ReasoningEfforts[1].Key != "LOW" ||
		response.Data.ReasoningEfforts[2].Key != "MEDIUM" ||
		response.Data.ReasoningEfforts[3].Key != "HIGH" {
		t.Fatalf("unexpected reasoning efforts %#v", response.Data.ReasoningEfforts)
	}
	foundCoderModel := false
	for _, model := range response.Data.Models {
		if model.Key == "coder-model" && model.Name == "Coder Model" && model.IsReasoner && model.IsVision && model.ContextWindow == 200000 {
			foundCoderModel = true
		}
	}
	if !foundCoderModel {
		t.Fatalf("expected coder model option, got %#v", response.Data.Models)
	}
}

func TestCoderModelOptionsFiltersEmptyAPIKeyAndShowsACPPassthrough(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "providers", "mock.yml"), []byte(strings.Join([]string{
				"key: mock",
				"baseUrl: http://127.0.0.1:1",
				"apiKey:",
				"defaultModel: mock-model",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write empty api key provider: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "gpt-5-codex.yml"), []byte(strings.Join([]string{
				"key: gpt-5-codex",
				"name: GPT-5 Codex",
				"protocol: ACP_PASSTHROUGH",
				"modelId: gpt-5-codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp passthrough model: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if response.Data.DefaultModelKey != "gpt-5-codex" {
		t.Fatalf("expected ACP passthrough fallback default, got %#v", response.Data)
	}
	for _, model := range response.Data.Models {
		if model.Key == "mock-model" {
			t.Fatalf("mock-model should be hidden when provider apiKey is empty: %#v", response.Data.Models)
		}
	}
	foundACP := false
	for _, model := range response.Data.Models {
		if model.Key == "gpt-5-codex" && model.Protocol == "ACP_PASSTHROUGH" && model.Provider == "" {
			foundACP = true
		}
	}
	if !foundACP {
		t.Fatalf("expected ACP passthrough model option, got %#v", response.Data.Models)
	}
}

func TestCoderModelOptionsDefaultSkipsHiddenProviderModel(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "providers", "mock.yml"), []byte(strings.Join([]string{
				"key: mock",
				"baseUrl: http://127.0.0.1:1",
				"apiKey:",
				"defaultModel: mock-model",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write empty api key provider: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "providers", "ready.yml"), []byte(strings.Join([]string{
				"key: ready",
				"baseUrl: http://127.0.0.1:1",
				"apiKey: ready-key",
				"defaultModel: ready-model",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write ready provider: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "ready-model.yml"), []byte(strings.Join([]string{
				"key: ready-model",
				"name: Ready Model",
				"provider: ready",
				"protocol: OPENAI",
				"modelId: ready-model-id",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write ready model: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if response.Data.DefaultModelKey != "ready-model" {
		t.Fatalf("expected ready-model default, got %#v", response.Data)
	}
	for _, model := range response.Data.Models {
		if model.Key == "mock-model" {
			t.Fatalf("mock-model should be hidden when provider apiKey is empty: %#v", response.Data.Models)
		}
	}
}

func TestCoderModelOptionsWS(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			setupCoderRuntime(t, cfg)
		},
	})
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readAutomationConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/model-options",
		ID:    "coder-options",
	}); err != nil {
		t.Fatalf("write options request: %v", err)
	}
	var optionsFrame ws.ResponseFrame
	if err := conn.ReadJSON(&optionsFrame); err != nil {
		t.Fatalf("read options response: %v", err)
	}
	options, err := marshalAgentResponseData[api.CoderModelOptionsResponse](optionsFrame.Data)
	if err != nil {
		t.Fatalf("decode options data: %v", err)
	}
	if optionsFrame.Frame != ws.FrameResponse || optionsFrame.ID != "coder-options" ||
		options.DefaultModelKey != "mock-model" || options.DefaultReasoningEffort != "MEDIUM" || len(options.ReasoningEfforts) != 4 {
		t.Fatalf("unexpected options frame %#v data=%#v", optionsFrame, options)
	}
}

func TestQueryModelOptionsValidation(t *testing.T) {
	t.Run("unknown model rejects request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"missing-model"}}`))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("providerless model rejects native request model", func(t *testing.T) {
		fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
			writeProviderSSE(t, w, `[DONE]`)
		}, testFixtureOptions{
			setupRuntime: func(_ string, cfg *config.Config) {
				modelPath := filepath.Join(cfg.Paths.RegistriesDir, "models", "gpt-5-codex.yml")
				if err := os.WriteFile(modelPath, []byte(strings.Join([]string{
					"key: gpt-5-codex",
					"name: GPT-5 Codex",
					"modelId: gpt-5-codex",
				}, "\n")), 0o644); err != nil {
					t.Fatalf("write providerless model config: %v", err)
				}
			},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"gpt-5-codex"}}`))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provider") {
			t.Fatalf("expected provider-backed rejection, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid reasoning effort rejects request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"reasoningEffort":"FAST"}}`))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("none reasoning effort rejects non coder request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"reasoningEffort":"NONE"}}`))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestQueryModelOptionsOverrideModelAndReasoningForRun(t *testing.T) {
	var requestBody atomic.Value
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		requestBody.Store(payload)
		writeAnthropicProviderSSE(t, w,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			setupCoderRuntime(t, cfg)
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"coder-model","reasoningEffort":"HIGH"},"params":{"channel":"business","hitlLevel":9,"memoryContext":"business-memory"}}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload, _ := requestBody.Load().(map[string]any)
	if payload["model"] != "coder-model-id" {
		t.Fatalf("expected provider model override, got %#v", payload)
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || int(thinking["budget_tokens"].(float64)) != 4096 {
		t.Fatalf("expected high reasoning thinking config, got %#v", payload)
	}

	messages := decodeSSEMessages(t, rec.Body.String())
	if len(messages) == 0 || messages[0]["type"] != "request.query" {
		t.Fatalf("expected first message request.query, got %#v", messages)
	}
	model, _ := messages[0]["model"].(map[string]any)
	if model["key"] != "coder-model" || model["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected request.query model options, got %#v", messages[0])
	}
	params, _ := messages[0]["params"].(map[string]any)
	if params["channel"] != "business" || params["hitlLevel"].(float64) != 9 || params["memoryContext"] != "business-memory" {
		t.Fatalf("expected params to remain business payload, got %#v", messages[0])
	}
}

func TestQueryModelOptionsNoneDisablesCoderReasoningForRun(t *testing.T) {
	var requestBody atomic.Value
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		requestBody.Store(payload)
		writeAnthropicProviderSSE(t, w,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			setupCoderRuntime(t, cfg)
		},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"coder-model","reasoningEffort":"NONE"}}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload, _ := requestBody.Load().(map[string]any)
	if payload["model"] != "coder-model-id" {
		t.Fatalf("expected provider model override, got %#v", payload)
	}
	if _, ok := payload["thinking"]; ok {
		t.Fatalf("expected NONE reasoning to omit thinking config, got %#v", payload)
	}
}
