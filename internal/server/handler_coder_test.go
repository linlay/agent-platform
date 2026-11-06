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
	workspace := setupCoderTestWorkspace(t, cfg, "mock-coder-workspace")
	modelPath := filepath.Join(cfg.Paths.RegistriesDir, "models", "coder-model.yml")
	if err := os.WriteFile(modelPath, []byte(strings.Join([]string{
		"key: coder-model",
		"name: Coder Model",
		"icon: Coder Model Icon",
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
		"  workspaceRoot: " + filepath.ToSlash(workspace),
		"mode: CODER",
		"stageSettings:",
		"  execute:",
		"    reasoningEffort: LOW",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write coder agent config: %v", err)
	}
}

func setupCoderTestWorkspace(t *testing.T, cfg *config.Config, name string) string {
	t.Helper()
	workspace := filepath.Join(filepath.Dir(cfg.Paths.AgentsDir), name)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir coder workspace: %v", err)
	}
	return workspace
}

func TestCoderModelOptionsHTTP(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			setupCoderRuntime(t, cfg)
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "vl-model.yml"), []byte(strings.Join([]string{
				"key: vl-model",
				"provider: mock",
				"type: vl",
				"modelId: qwen-vl-max",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write vl model config: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "embedding-model.yml"), []byte(strings.Join([]string{
				"key: embedding-model",
				"provider: mock",
				"type: embedding",
				"modelId: text-embedding-v4",
				"embedding:",
				"  dimension: 1024",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write embedding model config: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "image-model.yml"), []byte(strings.Join([]string{
				"key: image-model",
				"provider: mock",
				"type: image",
				"modelId: gpt-image-1",
				"image:",
				"  generation:",
				"    endpointPath: /v1/images/generations",
				"    requestFormat: openai-images-json",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write image model config: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var wireResponse api.ApiResponse[json.RawMessage]
	if err := json.Unmarshal(rec.Body.Bytes(), &wireResponse); err != nil {
		t.Fatalf("decode options wire response: %v", err)
	}
	assertNativeModelOptionsJSON(t, wireResponse.Data)
	for _, field := range []string{"defaultModelKey", "defaultReasoningEffort", "defaultServiceTier", "selectedModelKey", "selectedReasoningEffort", "selectedServiceTier"} {
		if strings.Contains(rec.Body.String(), "\""+field+"\"") {
			t.Fatalf("options must not expose selection %s: %s", field, rec.Body.String())
		}
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if len(response.Data.ReasoningEfforts) != 6 ||
		response.Data.ReasoningEfforts[0].Key != "NONE" ||
		response.Data.ReasoningEfforts[1].Key != "LOW" ||
		response.Data.ReasoningEfforts[2].Key != "MEDIUM" ||
		response.Data.ReasoningEfforts[3].Key != "HIGH" ||
		response.Data.ReasoningEfforts[4].Key != "XHIGH" ||
		response.Data.ReasoningEfforts[5].Key != "MAX" {
		t.Fatalf("unexpected reasoning efforts %#v", response.Data.ReasoningEfforts)
	}
	foundCoderModel := false
	for _, model := range response.Data.Models {
		if model.Key == "coder-model" && model.Name == "Coder Model" && model.Icon == "Coder Model Icon" && model.Provider == "mock" {
			foundCoderModel = true
		}
		if model.Key == "embedding-model" || model.Key == "image-model" || model.Key == "vl-model" {
			t.Fatalf("non-chat model should not appear in model options: %#v", response.Data.Models)
		}
	}
	if !foundCoderModel {
		t.Fatalf("expected coder model option, got %#v", response.Data.Models)
	}
}

func TestCoderModelOptionsFiltersEmptyAPIKey(t *testing.T) {
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
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"defaultModelKey", "defaultReasoningEffort", "defaultServiceTier", "selectedModelKey", "selectedReasoningEffort", "selectedServiceTier"} {
		if strings.Contains(rec.Body.String(), "\""+field+"\"") {
			t.Fatalf("options must not expose selection %s: %s", field, rec.Body.String())
		}
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	for _, model := range response.Data.Models {
		if model.Key == "mock-model" {
			t.Fatalf("mock-model should be hidden when provider apiKey is empty: %#v", response.Data.Models)
		}
	}
}

func TestAgentDetailDoesNotEmbedModelOptionsAndExposesEngineIdentity(t *testing.T) {

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			workspace := setupCoderTestWorkspace(t, cfg, "shared-coder-workspace")
			for key, body := range map[string]string{
				"codex-agent": strings.Join([]string{
					"key: codex-agent",
					"name: Codex Agent",
					"engine: acp",
					"runtimeConfig:",
					"  engineId: fixture/agent",
					"  workspaceRoot: " + filepath.ToSlash(workspace),
				}, "\n"),
				"native-agent": strings.Join([]string{
					"key: native-agent",
					"name: Native Agent",
					"mode: CODER",
					"modelConfig:",
					"  modelKey: mock-model",
					"runtimeConfig:",
					"  workspaceRoot: " + filepath.ToSlash(workspace),
				}, "\n"),
			} {
				agentDir := filepath.Join(cfg.Paths.AgentsDir, key)
				if err := os.MkdirAll(agentDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", key, err)
				}
				if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(body), 0o644); err != nil {
					t.Fatalf("write %s: %v", key, err)
				}
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=codex-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent detail returned %d: %s", rec.Code, rec.Body.String())
	}
	var acpResponse api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &acpResponse); err != nil {
		t.Fatalf("decode acp agent detail: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "\"engineId\":\"fixture/agent\"") {
		t.Fatalf("usage response must expose the external engine identity: %s", rec.Body.String())
	}
	if acpResponse.Data.ReasoningEffort != "" {
		t.Fatalf("expected no project reasoning override, got %q", acpResponse.Data.ReasoningEffort)
	}
	if acpResponse.Data.ModelKey != "" {
		t.Fatalf("must retain configured model: %#v", acpResponse.Data)
	}
	for _, field := range []string{"modelOptions", "modelConfig", "selectedModelKey"} {
		if strings.Contains(rec.Body.String(), "\""+field+"\"") {
			t.Fatalf("unexpected field %s", field)
		}
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=native-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("native agent detail returned %d: %s", rec.Code, rec.Body.String())
	}
	var nativeResponse api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &nativeResponse); err != nil {
		t.Fatalf("decode native agent detail: %v", err)
	}
	if strings.Contains(rec.Body.String(), `"modelOptions"`) {
		t.Fatalf("native agent detail should omit modelOptions, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agents returned %d: %s", rec.Code, rec.Body.String())
	}
	var summaries api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode agent summaries: %v", err)
	}
	var acpSummary *api.AgentSummary
	var nativeSummary *api.AgentSummary
	for i := range summaries.Data {
		switch summaries.Data[i].Key {
		case "codex-agent":
			acpSummary = &summaries.Data[i]
		case "native-agent":
			nativeSummary = &summaries.Data[i]
		}
	}
	if acpSummary == nil || nativeSummary == nil {
		t.Fatalf("expected acp and native summaries, got %#v", summaries.Data)
	}
	if acpSummary.ModelOptions != nil || acpSummary.ModelConfig != nil {
		t.Fatalf("ACP agent summary should omit model options/config, got %#v", acpSummary)
	}
	if acpSummary.DefaultModelKey != "" {
		t.Fatalf("ACP summary default model should remain local config value, got %#v", acpSummary)
	}
	if nativeSummary.ModelOptions != nil || nativeSummary.ModelConfig != nil {
		t.Fatalf("native agent summary should not include ACP model config/options, got %#v", nativeSummary)
	}
}

func serviceTierKeys(options []api.ServiceTierOption) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		out = append(out, option.Key)
	}
	return out
}

func reasoningEffortKeys(options []api.ReasoningEffortOption) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		out = append(out, option.Key)
	}
	return out
}

func TestCoderModelOptionsForNativeCoderAgent(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			workspace := setupCoderTestWorkspace(t, cfg, "native-workspace")
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
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "native-agent")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir native agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: native-agent",
				"name: Native Agent",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: ready-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write native agent: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options?agentKey=native-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"defaultModelKey", "defaultReasoningEffort", "defaultServiceTier", "selectedModelKey", "selectedReasoningEffort", "selectedServiceTier"} {
		if strings.Contains(rec.Body.String(), "\""+field+"\"") {
			t.Fatalf("options must not expose selection %s: %s", field, rec.Body.String())
		}
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	foundReady := false
	for _, model := range response.Data.Models {
		if model.Key == "ready-model" {
			foundReady = true
		}
	}
	if !foundReady {
		t.Fatalf("expected ready provider model to stay visible, got %#v", response.Data.Models)
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
	for _, field := range []string{"defaultModelKey", "defaultReasoningEffort", "defaultServiceTier", "selectedModelKey", "selectedReasoningEffort", "selectedServiceTier"} {
		if strings.Contains(rec.Body.String(), "\""+field+"\"") {
			t.Fatalf("options must not expose selection %s: %s", field, rec.Body.String())
		}
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
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
	wireData, err := json.Marshal(optionsFrame.Data)
	if err != nil {
		t.Fatalf("encode options wire data: %v", err)
	}
	assertNativeModelOptionsJSON(t, wireData)
	options, err := marshalAgentResponseData[api.CoderModelOptionsResponse](optionsFrame.Data)
	if err != nil {
		t.Fatalf("decode options data: %v", err)
	}
	if optionsFrame.Frame != ws.FrameResponse || optionsFrame.ID != "coder-options" ||
		len(options.ReasoningEfforts) != 6 {
		t.Fatalf("unexpected options frame %#v data=%#v", optionsFrame, options)
	}
	foundIcon := false
	for _, model := range options.Models {
		if model.Key == "coder-model" {
			foundIcon = model.Icon == "Coder Model Icon"
		}
	}
	if !foundIcon {
		t.Fatalf("expected WebSocket model icon, got %#v", options.Models)
	}
}

func assertNativeModelOptionsJSON(t *testing.T, data []byte) {
	t.Helper()
	var options struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(data, &options); err != nil {
		t.Fatalf("decode model options JSON: %v", err)
	}
	if len(options.Models) == 0 {
		t.Fatal("expected native model options")
	}
	for _, model := range options.Models {
		for _, field := range []string{"modelId", "protocol", "isReasoner", "isVision", "contextWindow", "timeout", "reasoningEfforts"} {
			if _, exists := model[field]; exists {
				t.Fatalf("native model option must omit %s: %s", field, data)
			}
		}
	}
}

func TestQueryModelOptionsValidation(t *testing.T) {
	t.Run("unknown model rejects request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/query?detail=detailed", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"missing-model"}}`))
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
		req := httptest.NewRequest(http.MethodPost, "/api/query?detail=detailed", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"gpt-5-codex"}}`))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provider") {
			t.Fatalf("expected provider-backed rejection, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid reasoning effort rejects request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/query?detail=detailed", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"reasoningEffort":"FAST"}}`))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	for _, effort := range []string{"XHIGH", "MAX"} {
		t.Run(strings.ToLower(effort)+" reasoning effort accepts native request model", func(t *testing.T) {
			fixture := newTestFixture(t)
			def, ok := fixture.registry.AgentDefinition("mock-agent")
			if !ok {
				t.Fatal("mock-agent not found")
			}
			if err := fixture.server.validateQueryModelOptions(&api.QueryModelOptions{ReasoningEffort: effort}, def); err != nil {
				t.Fatalf("native %s reasoning effort rejected: %v", effort, err)
			}
		})
	}

	t.Run("extra high compatibility alias normalizes in request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		def, ok := fixture.registry.AgentDefinition("mock-agent")
		if !ok {
			t.Fatal("mock-agent not found")
		}
		options := &api.QueryModelOptions{ReasoningEffort: "extra_high"}
		if err := fixture.server.validateQueryModelOptions(options, def); err != nil {
			t.Fatalf("EXTRA_HIGH reasoning effort rejected: %v", err)
		}
		if options.ReasoningEffort != "XHIGH" {
			t.Fatalf("request effort=%q want XHIGH", options.ReasoningEffort)
		}
	})

	t.Run("none reasoning effort accepts native non coder request model", func(t *testing.T) {
		fixture := newTestFixture(t)
		def, ok := fixture.registry.AgentDefinition("mock-agent")
		if !ok {
			t.Fatal("mock-agent not found")
		}
		if err := fixture.server.validateQueryModelOptions(&api.QueryModelOptions{ReasoningEffort: "NONE"}, def); err != nil {
			t.Fatalf("native NONE reasoning effort rejected: %v", err)
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
	req := httptest.NewRequest(http.MethodPost, "/api/query?detail=detailed", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"coder-model","reasoningEffort":"HIGH"},"params":{"channel":"business","hitlLevel":9,"memoryContext":"business-memory"}}`))
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
	outputConfig, _ := payload["output_config"].(map[string]any)
	if len(thinking) != 2 || thinking["type"] != "adaptive" || thinking["display"] != "summarized" || outputConfig["effort"] != "high" {
		t.Fatalf("expected high reasoning thinking config, got %#v", payload)
	}

	messages := decodeSSEMessages(t, rec.Body.String())
	requestQuery := findSSEMessageByType(t, messages, "request.query")
	model, _ := requestQuery["model"].(map[string]any)
	if model["key"] != "coder-model" || model["reasoningEffort"] != "HIGH" {
		t.Fatalf("expected request.query model options, got %#v", requestQuery)
	}
	params, _ := requestQuery["params"].(map[string]any)
	if params["channel"] != "business" || params["hitlLevel"].(float64) != 9 || params["memoryContext"] != "business-memory" {
		t.Fatalf("expected params to remain business payload, got %#v", requestQuery)
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
	req := httptest.NewRequest(http.MethodPost, "/api/query?detail=detailed", bytes.NewBufferString(`{"message":"hi","agentKey":"mock-agent","model":{"key":"coder-model","reasoningEffort":"NONE"}}`))
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
