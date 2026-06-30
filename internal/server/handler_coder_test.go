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

func TestCoderModelOptionsFiltersEmptyAPIKeyAndHidesACPPassthroughFromPublicOptions(t *testing.T) {
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
	if response.Data.DefaultModelKey != "" {
		t.Fatalf("expected no public default when only hidden models remain, got %#v", response.Data)
	}
	for _, model := range response.Data.Models {
		if model.Key == "mock-model" {
			t.Fatalf("mock-model should be hidden when provider apiKey is empty: %#v", response.Data.Models)
		}
	}
	for _, model := range response.Data.Models {
		if model.Key == "gpt-5-codex" {
			t.Fatalf("ACP passthrough model should be hidden from public options: %#v", response.Data.Models)
		}
	}
}

func TestCoderModelOptionsForACPCoderAgentOnlyShowsACPPassthrough(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
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
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "gpt-5.5.yml"), []byte(strings.Join([]string{
				"key: gpt-5.5",
				"name: GPT-5.5",
				"protocol: ACP_PASSTHROUGH",
				"modelId: gpt-5.5",
				"serviceTiers:",
				"  - fast",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp model: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "gpt-5.4.yml"), []byte(strings.Join([]string{
				"key: gpt-5.4",
				"name: GPT-5.4",
				"protocol: ACP_PASSTHROUGH",
				"modelId: gpt-5.4",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write alternate acp model: %v", err)
			}
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "codex-agent")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir acp agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: codex-agent",
				"name: Codex Agent",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: gpt-5.5",
				"runtimeConfig:",
				"  acpProxyId: codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp agent: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options?agentKey=codex-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if response.Data.DefaultModelKey != "gpt-5.5" {
		t.Fatalf("expected acp default, got %#v", response.Data)
	}
	if response.Data.DefaultServiceTier != "STANDARD" {
		t.Fatalf("expected acp default service tier STANDARD, got %#v", response.Data)
	}
	if got := strings.Join(serviceTierKeys(response.Data.ServiceTiers), ","); got != "STANDARD,FAST" {
		t.Fatalf("service tier options = %#v", response.Data.ServiceTiers)
	}
	for _, model := range response.Data.Models {
		if model.Key == "ready-model" {
			t.Fatalf("expected provider model to be hidden for acp coder agent, got %#v", response.Data.Models)
		}
	}
	if len(response.Data.Models) != 2 {
		t.Fatalf("expected only acp models, got %#v", response.Data.Models)
	}
	for _, model := range response.Data.Models {
		if model.Key == "gpt-5.5" && (len(model.ServiceTiers) != 1 || model.ServiceTiers[0] != "fast") {
			t.Fatalf("expected gpt-5.5 fast service tier, got %#v", model.ServiceTiers)
		}
	}
}

func TestCoderModelOptionsForACPCoderAgentUsesProxyModelDiscovery(t *testing.T) {
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "success",
			"data": map[string]any{
				"models": []map[string]any{
					{
						"key":              "MiniMax-M2.7",
						"name":             "MiniMax-M2.7",
						"modelId":          "MiniMax-M2.7",
						"contextWindow":    200000,
						"isReasoner":       true,
						"reasoningEfforts": []string{"LOW", "MEDIUM", "HIGH", "XHIGH"},
						"serviceTiers":     []string{"FAST"},
					},
				},
			},
		}); err != nil {
			t.Fatalf("encode proxy model response: %v", err)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"codex": {BaseURL: upstream.URL, Timeout: 5},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "codex-agent")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir acp agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: codex-agent",
				"name: Codex Agent",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: claude-opus-4-6",
				"runtimeConfig:",
				"  acpProxyId: codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "claude-opus-4-6.yml"), []byte(strings.Join([]string{
				"key: claude-opus-4-6",
				"name: Old Local Model",
				"protocol: ACP_PASSTHROUGH",
				"modelId: stale-local-model",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write local fallback model: %v", err)
			}
		},
	})

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options?agentKey=codex-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if len(response.Data.Models) != 1 {
		t.Fatalf("models = %#v", response.Data.Models)
	}
	if response.Data.DefaultServiceTier != "STANDARD" {
		t.Fatalf("expected acp default service tier STANDARD, got %#v", response.Data)
	}
	if got := strings.Join(serviceTierKeys(response.Data.ServiceTiers), ","); got != "STANDARD,FAST" {
		t.Fatalf("service tier options = %#v", response.Data.ServiceTiers)
	}
	if got := strings.Join(reasoningEffortKeys(response.Data.ReasoningEfforts), ","); got != "NONE,LOW,MEDIUM,HIGH,XHIGH" {
		t.Fatalf("reasoning effort options = %#v", response.Data.ReasoningEfforts)
	}
	model := response.Data.Models[0]
	if response.Data.DefaultModelKey != "MiniMax-M2.7" {
		t.Fatalf("default model should come from ACP /api/models, got %#v", response.Data)
	}
	if model.Key != "MiniMax-M2.7" || model.Name != "MiniMax-M2.7" || model.ModelID != "MiniMax-M2.7" || model.ContextWindow != 200000 || model.Protocol != "ACP_PASSTHROUGH" {
		t.Fatalf("unexpected proxy model %#v", model)
	}
	if strings.Join(model.ServiceTiers, ",") != "FAST" {
		t.Fatalf("service tiers = %#v", model.ServiceTiers)
	}
	if strings.Join(model.ReasoningEfforts, ",") != "LOW,MEDIUM,HIGH,XHIGH" {
		t.Fatalf("reasoning efforts = %#v", model.ReasoningEfforts)
	}
}

func TestAgentDetailIncludesModelOptionsOnlyForACPCoder(t *testing.T) {
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "success",
			"data": map[string]any{
				"models": []map[string]any{
					{
						"key":              "MiniMax-M2.7",
						"name":             "MiniMax-M2.7",
						"modelId":          "MiniMax-M2.7",
						"contextWindow":    200000,
						"isReasoner":       true,
						"reasoningEfforts": []string{"LOW", "MEDIUM", "HIGH", "XHIGH"},
						"serviceTiers":     []string{"FAST"},
					},
				},
			},
		}); err != nil {
			t.Fatalf("encode proxy model response: %v", err)
		}
	}))
	defer upstream.Close()

	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.ACPProxies = map[string]config.CoderACPProxyConfig{
				"codex": {BaseURL: upstream.URL, Timeout: 5},
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			for key, body := range map[string]string{
				"codex-agent": strings.Join([]string{
					"key: codex-agent",
					"name: Codex Agent",
					"mode: CODER",
					"modelConfig:",
					"  modelKey: claude-opus-4-6",
					"runtimeConfig:",
					"  acpProxyId: codex",
				}, "\n"),
				"native-agent": strings.Join([]string{
					"key: native-agent",
					"name: Native Agent",
					"mode: CODER",
					"modelConfig:",
					"  modelKey: mock-model",
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
	if acpResponse.Data.ModelOptions == nil || acpResponse.Data.ModelOptions.DefaultModelKey != "MiniMax-M2.7" {
		t.Fatalf("expected ACP detail model options, got %#v", acpResponse.Data.ModelOptions)
	}
	if len(acpResponse.Data.ModelOptions.Models) != 1 || acpResponse.Data.ModelOptions.Models[0].Protocol != "ACP_PASSTHROUGH" {
		t.Fatalf("expected ACP passthrough model options, got %#v", acpResponse.Data.ModelOptions.Models)
	}
	if acpResponse.Data.Model != "MiniMax-M2.7" || modelConfigString(acpResponse.Data.ModelConfig, "modelKey") != "MiniMax-M2.7" {
		t.Fatalf("expected ACP detail model config from /api/models, got model=%q config=%#v", acpResponse.Data.Model, acpResponse.Data.ModelConfig)
	}
	if acpResponse.Data.Meta["modelKey"] != "MiniMax-M2.7" {
		t.Fatalf("expected ACP detail meta modelKey from /api/models, got %#v", acpResponse.Data.Meta)
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
	if nativeResponse.Data.ModelOptions != nil {
		t.Fatalf("native agent detail should not include model options, got %#v", nativeResponse.Data.ModelOptions)
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
	if acpSummary.ModelOptions == nil || acpSummary.ModelOptions.DefaultModelKey != "MiniMax-M2.7" {
		t.Fatalf("expected ACP summary model options from /api/models, got %#v", acpSummary.ModelOptions)
	}
	if acpSummary.DefaultModelKey != "MiniMax-M2.7" || modelConfigString(acpSummary.ModelConfig, "modelKey") != "MiniMax-M2.7" {
		t.Fatalf("expected ACP summary model config from /api/models, got %#v", acpSummary)
	}
	if acpSummary.Meta["modelKey"] != "MiniMax-M2.7" {
		t.Fatalf("expected ACP summary meta modelKey from /api/models, got %#v", acpSummary.Meta)
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

func TestCoderModelOptionsForNativeCoderAgentHidesACPPassthrough(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
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
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "gpt-5.5.yml"), []byte(strings.Join([]string{
				"key: gpt-5.5",
				"name: GPT-5.5",
				"protocol: ACP_PASSTHROUGH",
				"modelId: gpt-5.5",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp model: %v", err)
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
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if response.Data.DefaultModelKey != "ready-model" {
		t.Fatalf("expected native default, got %#v", response.Data)
	}
	foundReady := false
	for _, model := range response.Data.Models {
		if model.Key == "gpt-5.5" {
			t.Fatalf("expected acp passthrough model to be hidden for native coder, got %#v", response.Data.Models)
		}
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
