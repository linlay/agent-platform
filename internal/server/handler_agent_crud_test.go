package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentHTTPCRUDAndEditableDetail(t *testing.T) {
	fixture := newTestFixture(t)

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "editable-agent",
		"definition": map[string]any{
			"key":         "editable-agent",
			"name":        "Editable Agent",
			"icon":        "bot",
			"role":        "Editor",
			"description": "editable test agent",
			"mode":        "REACT",
			"modelConfig": map[string]any{"modelKey": "mock-model"},
			"toolConfig":  map[string]any{"tools": []any{"datetime"}},
			"runtimeConfig": map[string]any{
				"environmentId": "shell",
				"level":         "RUN",
				"env":           map[string]any{"HTTP_PROXY": "http://agent-proxy"},
			},
		},
		"soulPrompt":   "Soul v1",
		"agentsPrompt": "Agents v1",
	})
	if created.Key != "editable-agent" || created.Source == nil || created.Source.Kind != "directory" {
		t.Fatalf("unexpected create response %#v", created)
	}
	if created.SoulPrompt != "Soul v1" || created.AgentsPrompt != "Agents v1" {
		t.Fatalf("expected prompts in response, got %#v", created)
	}
	if created.Definition["key"] != "editable-agent" {
		t.Fatalf("expected editable definition, got %#v", created.Definition)
	}
	if created.Definition["name"] != "Editable Agent" || created.Definition["icon"] != "bot" {
		t.Fatalf("expected non-CODER name and icon to be preserved, got %#v", created.Definition)
	}
	runtimeConfig, _ := created.Definition["runtimeConfig"].(map[string]any)
	env, _ := runtimeConfig["env"].(map[string]any)
	if env["HTTP_PROXY"] != "http://agent-proxy" {
		t.Fatalf("expected runtime env to be returned in editable detail, got %#v", created.Definition)
	}
	if !strings.HasSuffix(created.Source.Path, filepath.Join("editable-agent", "agent.yml")) {
		t.Fatalf("unexpected source path %q", created.Source.Path)
	}

	detail := getAdminAgentDetail(t, fixture.server, "editable-agent")
	if detail.SoulPrompt != "Soul v1" || detail.AgentsPrompt != "Agents v1" {
		t.Fatalf("expected prompts from detail, got %#v", detail)
	}

	updatedDefinition := detail.Definition
	updatedDefinition["description"] = "updated test agent"
	updated := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/update", map[string]any{
		"key":          "editable-agent",
		"definition":   updatedDefinition,
		"agentsPrompt": "Agents v2",
	})
	if updated.Description != "updated test agent" || updated.SoulPrompt != "Soul v1" || updated.AgentsPrompt != "Agents v2" {
		t.Fatalf("unexpected update response %#v", updated)
	}

	deleted := postAgentJSON[map[string]any](t, fixture.server, "/api/admin/agents/delete", map[string]any{"key": "editable-agent"})
	if deleted["key"] != "editable-agent" || deleted["deleted"] != true {
		t.Fatalf("unexpected delete response %#v", deleted)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=editable-agent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected deleted agent to be absent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentProxyCRUDAllowsProxyConfigWithoutModelConfig(t *testing.T) {
	fixture := newTestFixture(t)

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "proxy-agent",
		"definition": map[string]any{
			"key":         "proxy-agent",
			"name":        "Proxy Agent",
			"role":        "Proxy",
			"description": "proxy test agent",
			"mode":        "PROXY",
			"proxyConfig": map[string]any{
				"baseUrl": "http://127.0.0.1:3210",
				"token":   "proxy-token",
				"timeout": 300,
			},
		},
	})
	proxyConfig, _ := created.Definition["proxyConfig"].(map[string]any)
	if created.Mode != "PROXY" || proxyConfig["token"] != "proxy-token" {
		t.Fatalf("expected editable proxy detail with token, got %#v", created)
	}
	if created.Definition["mode"] != "PROXY" {
		t.Fatalf("expected PROXY to persist as PROXY, got %#v", created.Definition)
	}
}

func TestAgentPlanExecuteCRUDUsesAPIModeContract(t *testing.T) {
	fixture := newTestFixture(t)

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "plan-agent",
		"definition": map[string]any{
			"key":         "plan-agent",
			"name":        "Plan Agent",
			"role":        "Planner",
			"description": "plan execute test agent",
			"mode":        "PLAN-EXECUTE",
			"modelConfig": map[string]any{"modelKey": "mock-model"},
		},
	})
	if created.Mode != "PLAN-EXECUTE" || created.Definition["mode"] != "PLAN-EXECUTE" {
		t.Fatalf("expected PLAN-EXECUTE create response, got %#v", created)
	}

	detail := getAdminAgentDetail(t, fixture.server, "plan-agent")
	if detail.Mode != "PLAN-EXECUTE" || detail.Definition["mode"] != "PLAN-EXECUTE" {
		t.Fatalf("expected PLAN-EXECUTE detail response, got %#v", detail)
	}

	detail.Definition["description"] = "updated plan execute test agent"
	updated := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/update", map[string]any{
		"key":        "plan-agent",
		"definition": detail.Definition,
	})
	if updated.Mode != "PLAN-EXECUTE" || updated.Definition["mode"] != "PLAN-EXECUTE" {
		t.Fatalf("expected PLAN-EXECUTE update response, got %#v", updated)
	}
}

func TestAgentCreateKBaseGeneratesKeyAndName(t *testing.T) {
	fixture := newTestFixture(t)
	workspaceDir := filepath.Join(t.TempDir(), "knowledge-base-alpha")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	beforeCreate := time.Now().Unix()
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"definition": map[string]any{
			"mode": "KBASE",
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
		},
	})
	afterCreate := time.Now().Unix()
	if !strings.HasPrefix(created.Key, "kbase-") || created.Mode != "KBASE" {
		t.Fatalf("unexpected kbase create response %#v", created)
	}
	generatedAt, err := strconv.ParseInt(strings.TrimPrefix(created.Key, "kbase-"), 36, 64)
	if err != nil {
		t.Fatalf("kbase key suffix should be base36 seconds, key=%q err=%v", created.Key, err)
	}
	if generatedAt < beforeCreate || generatedAt > afterCreate {
		t.Fatalf("kbase key suffix = %d, want between %d and %d for key %q", generatedAt, beforeCreate, afterCreate, created.Key)
	}
	if created.Definition["key"] != created.Key {
		t.Fatalf("expected generated kbase key to be persisted, key=%q definition=%#v", created.Key, created.Definition["key"])
	}
	name, nameOk := created.Definition["name"].(string)
	if !nameOk || name != filepath.Base(workspaceDir) {
		t.Fatalf("kbase definition name = %#v, want %q", created.Definition["name"], filepath.Base(workspaceDir))
	}
	if created.Source == nil {
		t.Fatalf("expected created source")
	}
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatalf("read created agent file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 || lines[0] != "key: "+created.Key || lines[1] != "name: knowledge-base-alpha" || lines[2] != "mode: KBASE" {
		t.Fatalf("unexpected YAML header order:\n%s", data)
	}
}

func TestAgentCreateCoderAndOpenWorkspace(t *testing.T) {
	fixture := newTestFixture(t)
	workspaceDir := filepath.Join(t.TempDir(), "project-alpha")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	beforeCreate := time.Now().Unix()
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"definition": map[string]any{
			"name": "agent-coder",
			"icon": "bot",
			"mode": "CODER",
			"workspace": map[string]any{
				"root": workspaceDir,
			},
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
			"visibility": map[string]any{
				"scopes": []any{"nav", "copilot"},
			},
		},
	})
	afterCreate := time.Now().Unix()
	if !strings.HasPrefix(created.Key, "coder-") || created.Mode != "CODER" {
		t.Fatalf("unexpected coder create response %#v", created)
	}
	generatedAt, err := strconv.ParseInt(strings.TrimPrefix(created.Key, "coder-"), 36, 64)
	if err != nil {
		t.Fatalf("coder key suffix should be base36 seconds, key=%q err=%v", created.Key, err)
	}
	if generatedAt < beforeCreate || generatedAt > afterCreate {
		t.Fatalf("coder key suffix = %d, want between %d and %d for key %q", generatedAt, beforeCreate, afterCreate, created.Key)
	}
	if created.Definition["key"] != created.Key {
		t.Fatalf("expected generated coder key to be persisted, key=%q definition=%#v", created.Key, created.Definition["key"])
	}
	if _, ok := created.Definition["workspace"]; ok {
		t.Fatalf("coder definition should not persist workspace root shortcut, got %#v", created.Definition)
	}
	name, nameOk := created.Definition["name"].(string)
	if !nameOk || name == "" {
		t.Fatalf("coder definition name missing or empty: %#v", created.Definition["name"])
	}
	if name != filepath.Base(workspaceDir) {
		t.Fatalf("coder definition name = %q, want %q", name, filepath.Base(workspaceDir))
	}
	icon, iconOk := created.Definition["icon"].(map[string]any)
	if !iconOk || icon["name"] != "folder" {
		t.Fatalf("coder definition icon = %#v, want {name: folder}", created.Definition["icon"])
	}
	visibility, _ := created.Definition["visibility"].(map[string]any)
	scopes, _ := visibility["scopes"].([]any)
	if len(scopes) != 1 || scopes[0] != "nav" {
		t.Fatalf("coder visibility scopes = %#v, want [nav]", visibility["scopes"])
	}
	if _, ok := created.Definition["concurrency"]; ok {
		t.Fatalf("coder definition should not persist concurrency, got %#v", created.Definition["concurrency"])
	}
	if created.Source == nil {
		t.Fatalf("expected created source")
	}
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatalf("read created agent file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 || lines[0] != "key: "+created.Key || lines[1] != "name: project-alpha" || lines[2] != "mode: CODER" {
		t.Fatalf("unexpected YAML header order:\n%s", data)
	}
	if !strings.Contains(string(data), "\nname:") || strings.Contains(string(data), "\nworkspace:") || strings.Contains(string(data), "- copilot") {
		t.Fatalf("created coder file should include name, omit workspace and copilot scope:\n%s", data)
	}
	if strings.Contains(string(data), "\nconcurrency:") {
		t.Fatalf("created coder file should omit concurrency:\n%s", data)
	}
	if !strings.Contains(string(data), "\n  name: folder\n") {
		t.Fatalf("created coder file should persist icon.name: folder:\n%s", data)
	}

	updatedDefinition := created.Definition
	updatedDefinition["name"] = "Renamed Coder"
	updatedDefinition["icon"] = "sparkles"
	updated := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/update", map[string]any{
		"key":        created.Key,
		"definition": updatedDefinition,
	})
	updatedName, _ := updated.Definition["name"].(string)
	if updatedName != "Renamed Coder" {
		t.Fatalf("updated coder definition should persist name, got %#v", updated.Definition["name"])
	}
	updatedIcon, updatedIconOk := updated.Definition["icon"].(map[string]any)
	if !updatedIconOk || updatedIcon["name"] != "folder" {
		t.Fatalf("updated coder definition icon = %#v, want {name: folder}", updated.Definition["icon"])
	}
	updatedData, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatalf("read updated agent file: %v", err)
	}
	if !strings.Contains(string(updatedData), "\nname: Renamed Coder\n") || !strings.Contains(string(updatedData), "\n  name: folder\n") {
		t.Fatalf("updated coder file should persist name and icon.name: folder:\n%s", updatedData)
	}

	var openedPath string
	previousOpen := openWorkspacePath
	openWorkspacePath = func(path string) error {
		openedPath = path
		return nil
	}
	t.Cleanup(func() { openWorkspacePath = previousOpen })

	opened := postAgentJSON[api.OpenAgentWorkspaceResponse](t, fixture.server, "/api/agent/open-workspace", map[string]any{
		"agentKey": created.Key,
	})
	if !opened.Opened || opened.WorkspaceDir != workspaceDir || openedPath != workspaceDir {
		t.Fatalf("unexpected open response=%#v openedPath=%q", opened, openedPath)
	}
}

func TestAgentCreateCoderAppliesDefaultModelConfig(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.DefaultAgent = config.CoderDefaultAgentConfig{
				ModelKey:        "mock-model",
				ReasoningEffort: "MEDIUM",
			}
		},
	})
	workspaceDir := t.TempDir()

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "coder-defaults",
		"definition": map[string]any{
			"key":  "coder-defaults",
			"name": "coder-defaults",
			"mode": "CODER",
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
		},
	})
	modelConfig, _ := created.Definition["modelConfig"].(map[string]any)
	reasoning, _ := modelConfig["reasoning"].(map[string]any)
	if modelConfig["modelKey"] != "mock-model" || reasoning["effort"] != "MEDIUM" {
		t.Fatalf("expected coder default model config, got %#v", modelConfig)
	}
	if created.Meta["modelKey"] != "mock-model" {
		t.Fatalf("expected created coder model key mock-model, got %#v", created.Meta)
	}
}

func TestAgentCreateCoderPreservesExplicitModelConfig(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CoderSettings.DefaultAgent = config.CoderDefaultAgentConfig{
				ModelKey:        "default-model",
				ReasoningEffort: "HIGH",
			}
		},
	})
	workspaceDir := t.TempDir()

	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "coder-explicit",
		"definition": map[string]any{
			"key":  "coder-explicit",
			"name": "coder-explicit",
			"mode": "CODER",
			"modelConfig": map[string]any{
				"modelKey": "mock-model",
				"reasoning": map[string]any{
					"effort": "LOW",
				},
			},
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
		},
	})
	modelConfig, _ := created.Definition["modelConfig"].(map[string]any)
	reasoning, _ := modelConfig["reasoning"].(map[string]any)
	if modelConfig["modelKey"] != "mock-model" || reasoning["effort"] != "LOW" {
		t.Fatalf("expected explicit coder model config to win, got %#v", modelConfig)
	}
}

func TestAgentModelConfigUpdatePersistsCoderDefaults(t *testing.T) {
	fixture := newTestFixture(t)
	workspaceDir := t.TempDir()
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "coder-model-config",
		"definition": map[string]any{
			"key":  "coder-model-config",
			"name": "coder-model-config",
			"mode": "CODER",
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
		},
		"soulPrompt":   "Soul stays",
		"agentsPrompt": "Agents stay",
	})
	if created.Source == nil {
		t.Fatalf("expected created source")
	}

	body, err := json.Marshal(map[string]any{
		"agentKey":        created.Key,
		"modelKey":        "mock-model",
		"reasoningEffort": "HIGH",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agent/model-config", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("model config returned %d: %s", rec.Code, rec.Body.String())
	}
	var rawResponse api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResponse); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if len(rawResponse.Data) != 2 {
		t.Fatalf("expected compact model config response, got %#v", rawResponse.Data)
	}
	if rawResponse.Data["key"] != created.Key {
		t.Fatalf("expected response key, got %#v", rawResponse.Data)
	}
	rawModelConfig, _ := rawResponse.Data["modelConfig"].(map[string]any)
	rawReasoning, _ := rawModelConfig["reasoning"].(map[string]any)
	if rawModelConfig["modelKey"] != "mock-model" || rawReasoning["enabled"] != true || rawReasoning["effort"] != "HIGH" {
		t.Fatalf("expected compact persisted model config, got %#v", rawModelConfig)
	}
	modelConfig := rawModelConfig
	reasoning, _ := modelConfig["reasoning"].(map[string]any)
	if modelConfig["modelKey"] != "mock-model" || reasoning["enabled"] != true || reasoning["effort"] != "HIGH" {
		t.Fatalf("expected persisted model config, got %#v", modelConfig)
	}
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatalf("read updated agent file: %v", err)
	}
	if !strings.Contains(string(data), "modelKey: mock-model") ||
		!strings.Contains(string(data), "enabled: true") ||
		!strings.Contains(string(data), "effort: HIGH") {
		t.Fatalf("agent.yml did not persist model config:\n%s", data)
	}
}

func TestAgentModelConfigUpdatePersistsNoneReasoning(t *testing.T) {
	fixture := newTestFixture(t)
	workspaceDir := t.TempDir()
	created := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "coder-model-none",
		"definition": map[string]any{
			"key":  "coder-model-none",
			"name": "coder-model-none",
			"mode": "CODER",
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
		},
	})

	updated := postAgentJSON[api.AgentModelConfigResponse](t, fixture.server, "/api/agent/model-config", map[string]any{
		"key":             created.Key,
		"modelKey":        "mock-model",
		"reasoningEffort": "NONE",
	})
	modelConfig := updated.ModelConfig
	reasoning, _ := modelConfig["reasoning"].(map[string]any)
	if modelConfig["modelKey"] != "mock-model" || reasoning["enabled"] != false {
		t.Fatalf("expected NONE reasoning config, got %#v", modelConfig)
	}
	if _, ok := reasoning["effort"]; ok {
		t.Fatalf("NONE reasoning should omit effort, got %#v", reasoning)
	}
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatalf("read updated agent file: %v", err)
	}
	if strings.Contains(string(data), "effort:") {
		t.Fatalf("NONE reasoning should not persist effort:\n%s", data)
	}
	agents := fixture.server.deps.Registry.Agents("all")
	var matched api.AgentSummary
	for _, agent := range agents {
		if agent.Key == created.Key {
			matched = agent
			break
		}
	}
	if matched.DefaultModelKey != "mock-model" || matched.DefaultReasoningEffort != "NONE" {
		t.Fatalf("expected NONE defaults after reload, got %#v", matched)
	}
}

func TestAgentModelConfigUpdatePersistsACPServiceTierFromProxyModels(t *testing.T) {
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
						"key":          "gpt-5.5",
						"name":         "GPT-5.5",
						"modelId":      "gpt-5.5",
						"isReasoner":   true,
						"serviceTiers": []string{"FAST"},
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
				"runtimeConfig:",
				"  acpProxyId: codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp agent: %v", err)
			}
		},
	})

	updated := postAgentJSON[api.AgentModelConfigResponse](t, fixture.server, "/api/agent/model-config", map[string]any{
		"agentKey":        "codex-agent",
		"modelKey":        "gpt-5.5",
		"reasoningEffort": "LOW",
		"serviceTier":     "FAST",
	})
	if updated.ModelConfig["modelKey"] != "gpt-5.5" || updated.ModelConfig["serviceTier"] != "FAST" {
		t.Fatalf("expected ACP model config with service tier, got %#v", updated.ModelConfig)
	}
	data, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.AgentsDir, "codex-agent", "agent.yml"))
	if err != nil {
		t.Fatalf("read updated ACP agent file: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "modelKey: gpt-5.5") || !strings.Contains(text, "serviceTier: FAST") {
		t.Fatalf("agent.yml did not persist ACP service tier:\n%s", text)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/model-options?agentKey=codex-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("model options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CoderModelOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode model options response: %v", err)
	}
	if response.Data.DefaultServiceTier != "FAST" {
		t.Fatalf("expected ACP default service tier FAST, got %#v", response.Data)
	}
}

func TestAgentModelConfigUpdateRejectsUnsupportedACPServiceTier(t *testing.T) {
	upstream := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "success",
			"data": map[string]any{
				"models": []map[string]any{
					{
						"key":          "gpt-5.5",
						"name":         "GPT-5.5",
						"modelId":      "gpt-5.5",
						"isReasoner":   true,
						"serviceTiers": []string{"FAST"},
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
				"runtimeConfig:",
				"  acpProxyId: codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp agent: %v", err)
			}
		},
	})

	body, err := json.Marshal(map[string]any{
		"agentKey":        "codex-agent",
		"modelKey":        "gpt-5.5",
		"reasoningEffort": "LOW",
		"serviceTier":     "FLEX",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agent/model-config", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentModelConfigUpdateRejectsInvalidRequests(t *testing.T) {
	fixture := newTestFixture(t)
	workspaceDir := t.TempDir()
	createdCoder := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "coder-model-errors",
		"definition": map[string]any{
			"key":  "coder-model-errors",
			"name": "coder-model-errors",
			"mode": "CODER",
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
		},
	})
	postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"key": "react-model-errors",
		"definition": map[string]any{
			"key":         "react-model-errors",
			"name":        "react-model-errors",
			"mode":        "REACT",
			"modelConfig": map[string]any{"modelKey": "mock-model"},
		},
	})

	cases := []struct {
		name   string
		body   map[string]any
		status int
	}{
		{name: "missing agent", body: map[string]any{"agentKey": "missing-agent", "modelKey": "mock-model", "reasoningEffort": "HIGH"}, status: http.StatusNotFound},
		{name: "non coder", body: map[string]any{"agentKey": "react-model-errors", "modelKey": "mock-model", "reasoningEffort": "HIGH"}, status: http.StatusBadRequest},
		{name: "unknown model", body: map[string]any{"agentKey": createdCoder.Key, "modelKey": "missing-model", "reasoningEffort": "HIGH"}, status: http.StatusBadRequest},
		{name: "bad reasoning", body: map[string]any{"agentKey": createdCoder.Key, "modelKey": "mock-model", "reasoningEffort": "FAST"}, status: http.StatusBadRequest},
		{name: "service tier on non acp coder", body: map[string]any{"agentKey": createdCoder.Key, "modelKey": "mock-model", "reasoningEffort": "HIGH", "serviceTier": "FAST"}, status: http.StatusBadRequest},
		{name: "bad key", body: map[string]any{"agentKey": "../bad", "modelKey": "mock-model", "reasoningEffort": "HIGH"}, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agent/model-config", bytes.NewReader(body)))
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAgentOpenWorkspaceRejectsUnknownWorkspace(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"workspaceDir":"/tmp/not-an-agent-workspace"}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agent/open-workspace", body))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentEditorOptionsHTTP(t *testing.T) {
	fixture := newTestFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/editor-options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("options returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentEditorOptionsResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode options response: %v", err)
	}
	if len(response.Data.Models) != 1 || response.Data.Models[0].Key != "mock-model" || response.Data.Models[0].Name != "Mock Model" {
		t.Fatalf("expected mock model option, got %#v", response.Data.Models)
	}
	if got := response.Data.Modes; len(got) != 4 ||
		got[0].Key != "REACT" || got[0].Label != "REACT" ||
		got[1].Key != "PLAN-EXECUTE" || got[1].Label != "PLAN-EXECUTE" ||
		got[2].Key != "CODER" || got[2].Label != "CODER" ||
		got[3].Key != "PROXY" || got[3].Label != "PROXY" {
		t.Fatalf("unexpected modes %#v", got)
	}
	if len(response.Data.ContextTags) != 4 || response.Data.ContextTags[0].Key != "system" || response.Data.ContextTags[3].Key != "all-agents" {
		t.Fatalf("unexpected context tags %#v", response.Data.ContextTags)
	}
	if got := response.Data.VisibilityScopes; len(got) != 4 ||
		got[0].Key != "nav" || got[0].Label != "nav" ||
		got[1].Key != "copilot" || got[1].Label != "copilot" ||
		got[2].Key != "invoke" || got[2].Label != "invoke" ||
		got[3].Key != "internal" || got[3].Label != "internal" {
		t.Fatalf("unexpected visibility scopes %#v", got)
	}
	if response.Data.ProxyConfigSchema.DefaultTimeout != 300 || len(response.Data.ProxyConfigSchema.Fields) != 6 || !response.Data.ProxyConfigSchema.Fields[0].Required {
		t.Fatalf("unexpected proxy schema %#v", response.Data.ProxyConfigSchema)
	}
}

func TestAgentCRUDSafetyErrors(t *testing.T) {
	fixture := newTestFixture(t)

	cases := []struct {
		name   string
		path   string
		body   map[string]any
		status int
	}{
		{
			name: "duplicate",
			path: "/api/admin/agents/create",
			body: map[string]any{
				"key": "mock-agent",
				"definition": map[string]any{
					"key":         "mock-agent",
					"name":        "Duplicate",
					"description": "duplicate",
				},
			},
			status: http.StatusConflict,
		},
		{
			name: "missing key",
			path: "/api/admin/agents/create",
			body: map[string]any{
				"definition": map[string]any{"key": "", "name": "Missing"},
			},
			status: http.StatusBadRequest,
		},
		{
			name: "path traversal",
			path: "/api/admin/agents/create",
			body: map[string]any{
				"key": "../bad",
				"definition": map[string]any{
					"key":         "../bad",
					"name":        "Bad",
					"description": "bad",
				},
			},
			status: http.StatusBadRequest,
		},
		{
			name: "mismatched definition key",
			path: "/api/admin/agents/create",
			body: map[string]any{
				"key": "safe-key",
				"definition": map[string]any{
					"key":         "other-key",
					"name":        "Safe",
					"description": "safe",
				},
			},
			status: http.StatusBadRequest,
		},
		{
			name: "proxy missing base url",
			path: "/api/admin/agents/create",
			body: map[string]any{
				"key": "bad-proxy",
				"definition": map[string]any{
					"key":         "bad-proxy",
					"name":        "Bad Proxy",
					"description": "bad proxy",
					"mode":        "PROXY",
					"proxyConfig": map[string]any{"token": "token"},
				},
			},
			status: http.StatusBadRequest,
		},
		{
			name:   "delete missing",
			path:   "/api/admin/agents/delete",
			body:   map[string]any{"key": "missing-agent"},
			status: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body)))
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAgentWSRuntimeModelConfigAndAdminRoutesRejected(t *testing.T) {
	hub := ws.NewHub()
	t.Cleanup(func() { hub.CloseAll(gws.CloseNormalClosure, "test done") })
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: hub,
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})
	workspaceDir := t.TempDir()
	coderCreated := postAgentJSON[api.AgentDetailResponse](t, fixture.server, "/api/admin/agents/create", map[string]any{
		"definition": map[string]any{
			"name": "WS Coder",
			"mode": "CODER",
			"runtimeConfig": map[string]any{
				"workspaceRoot": workspaceDir,
			},
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

	for _, removed := range []struct {
		id        string
		frameType string
	}{
		{id: "admin-create", frameType: "/api/admin/agents/create"},
		{id: "admin-options", frameType: "/api/admin/agents/editor-options"},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{
			Frame: ws.FrameRequest,
			Type:  removed.frameType,
			ID:    removed.id,
		}); err != nil {
			t.Fatalf("write removed route request: %v", err)
		}
		var errFrame ws.ErrorFrame
		if err := conn.ReadJSON(&errFrame); err != nil {
			t.Fatalf("read removed route response: %v", err)
		}
		if errFrame.Frame != ws.FrameError || errFrame.Type != "invalid_request" || errFrame.ID != removed.id || errFrame.Code != http.StatusBadRequest ||
			!strings.Contains(errFrame.Msg, "unknown type: "+removed.frameType) {
			t.Fatalf("unexpected removed route frame for %s: %#v", removed.frameType, errFrame)
		}
	}

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/agent/model-config",
		ID:    "update-coder-model",
		Payload: ws.MarshalPayload(map[string]any{
			"agentKey":        coderCreated.Key,
			"modelKey":        "mock-model",
			"reasoningEffort": "NONE",
		}),
	}); err != nil {
		t.Fatalf("write model config request: %v", err)
	}
	var modelConfigFrame ws.ResponseFrame
	if err := conn.ReadJSON(&modelConfigFrame); err != nil {
		t.Fatalf("read model config response: %v", err)
	}
	modelUpdated, err := marshalAgentResponseData[api.AgentModelConfigResponse](modelConfigFrame.Data)
	if err != nil {
		t.Fatalf("decode model config data: %v", err)
	}
	modelConfig := modelUpdated.ModelConfig
	reasoning, _ := modelConfig["reasoning"].(map[string]any)
	if modelConfigFrame.Frame != ws.FrameResponse || modelConfigFrame.ID != "update-coder-model" ||
		modelConfig["modelKey"] != "mock-model" || reasoning["enabled"] != false {
		t.Fatalf("unexpected model config frame %#v data=%#v", modelConfigFrame, modelUpdated)
	}
}

func getAgentDetail(t *testing.T, server *Server, key string) api.AgentDetailResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey="+key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent detail returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	return response.Data
}

func getAdminAgentDetail(t *testing.T, server *Server, key string) api.AdminAgentDetailResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/detail?agentKey="+key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin agent detail returned %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode admin detail response: %v", err)
	}
	return response.Data
}

func postAgentJSON[T any](t *testing.T, server *Server, path string, payload any) T {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[T]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Data
}

func marshalAgentResponseData[T any](value any) (T, error) {
	var out T
	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
