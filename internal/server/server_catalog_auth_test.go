package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/llm"
)

func TestAgentEndpointReturnsDetail(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=mock-agent", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.AgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if response.Data.Key != "mock-agent" {
		t.Fatalf("expected mock-agent key, got %#v", response.Data)
	}
	if response.Data.Model != "mock-model-id" {
		t.Fatalf("expected resolved model id, got %#v", response.Data)
	}
	if response.Data.Mode != "REACT" {
		t.Fatalf("expected REACT mode, got %#v", response.Data)
	}
	wantWonders := []string{
		"帮我演示提问式确认",
		"帮我演示 Bash HITL 审批确认\n并说明用户接下来会看到什么",
	}
	if !reflect.DeepEqual(response.Data.Wonders, wantWonders) {
		t.Fatalf("expected wonders in detail response, got %#v", response.Data.Wonders)
	}
	if len(response.Data.Tools) != 6 ||
		response.Data.Tools[0] != "datetime" ||
		response.Data.Tools[1] != "ask_user_question" ||
		response.Data.Tools[2] != "bash" ||
		response.Data.Tools[3] != "memory_write" ||
		response.Data.Tools[4] != "memory_read" ||
		response.Data.Tools[5] != "memory_search" {
		t.Fatalf("expected tools in detail response, got %#v", response.Data.Tools)
	}
	if len(response.Data.Skills) != 1 || response.Data.Skills[0] != "mock-skill" {
		t.Fatalf("expected skills in detail response, got %#v", response.Data.Skills)
	}
	if len(response.Data.Controls) != 1 || response.Data.Controls[0]["key"] != "tone" {
		t.Fatalf("expected controls in detail response, got %#v", response.Data.Controls)
	}
	if response.Data.Meta["modelKey"] != "mock-model" {
		t.Fatalf("expected modelKey meta, got %#v", response.Data.Meta)
	}
	if response.Data.Meta["providerKey"] != "mock" {
		t.Fatalf("expected providerKey meta, got %#v", response.Data.Meta)
	}
	if response.Data.Meta["protocol"] != "OPENAI" {
		t.Fatalf("expected protocol meta, got %#v", response.Data.Meta)
	}
	modelKeys, ok := response.Data.Meta["modelKeys"].([]any)
	if !ok || len(modelKeys) != 1 || modelKeys[0] != "mock-model" {
		t.Fatalf("expected modelKeys meta, got %#v", response.Data.Meta["modelKeys"])
	}
	perAgentSkills, ok := response.Data.Meta["perAgentSkills"].([]any)
	if !ok || len(perAgentSkills) != 1 || perAgentSkills[0] != "mock-skill" {
		t.Fatalf("expected perAgentSkills meta, got %#v", response.Data.Meta["perAgentSkills"])
	}
	sandbox, ok := response.Data.Meta["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox meta, got %#v", response.Data.Meta)
	}
	if sandbox["level"] != "RUN" {
		t.Fatalf("expected sandbox level RUN, got %#v", sandbox["level"])
	}
	if _, exists := sandbox["env"]; exists {
		t.Fatalf("expected sandbox env to stay private, got %#v", sandbox)
	}
	extraMounts, ok := sandbox["extraMounts"].([]any)
	if !ok || len(extraMounts) != 1 {
		t.Fatalf("expected sandbox extraMounts, got %#v", sandbox)
	}
	firstMount, ok := extraMounts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first sandbox mount map, got %#v", extraMounts[0])
	}
	if _, exists := firstMount["source"]; !exists || firstMount["source"] != nil {
		t.Fatalf("expected sandbox mount source=null, got %#v", firstMount)
	}
	if firstMount["destination"] != "/skills" {
		t.Fatalf("expected sandbox mount destination /skills, got %#v", firstMount)
	}
}

func TestBuildAgentDetailMetaOmitsSandboxForRuntimeEnvOnly(t *testing.T) {
	s := &Server{}
	_, meta := s.buildAgentDetailMeta(catalog.AgentDefinition{
		ModelKey: "mock-model",
		Runtime: map[string]any{
			"env": map[string]string{"HTTP_PROXY": "http://127.0.0.1:8001"},
		},
	})
	if _, ok := meta["sandbox"]; ok {
		t.Fatalf("expected env-only runtime config to omit sandbox meta, got %#v", meta)
	}
}

func TestBuildAgentDetailMetaIncludesSandboxForRuntimeEnvironment(t *testing.T) {
	s := &Server{}
	_, meta := s.buildAgentDetailMeta(catalog.AgentDefinition{
		ModelKey: "mock-model",
		Runtime: map[string]any{
			"environmentId": "shell",
			"level":         "run",
			"env":           map[string]string{"HTTP_PROXY": "http://127.0.0.1:8001"},
		},
	})
	sandbox, ok := meta["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox meta, got %#v", meta)
	}
	if sandbox["environmentId"] != "shell" || sandbox["level"] != "RUN" {
		t.Fatalf("unexpected sandbox meta: %#v", sandbox)
	}
	if _, ok := sandbox["env"]; ok {
		t.Fatalf("expected runtime env to stay private, got %#v", sandbox)
	}
}

func TestBuildAgentDetailResponseIncludesCoderModeAndWorkspace(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "project")
	s := &Server{}
	response := s.buildAgentDetailResponse(catalog.AgentDefinition{
		Key:  "coder",
		Name: "Coder",
		Mode: catalog.AgentModeCoder,
		Workspace: catalog.AgentWorkspaceConfig{
			Root: workspace,
		},
		Project: catalog.AgentProjectConfig{
			PromptFiles: []catalog.AgentProjectPromptFile{
				{Source: "workspace", Path: "AGENTS.md"},
				{Source: "agent", Path: "AGENTS.md"},
			},
			Git: catalog.AgentProjectGitConfig{ExpectedBranch: "main"},
		},
	})
	if response.Mode != catalog.AgentModeCoder {
		t.Fatalf("mode = %q, want %q", response.Mode, catalog.AgentModeCoder)
	}
	if response.Type != "" {
		t.Fatalf("type = %q, want empty for CODER mode", response.Type)
	}
	workspaceMeta, ok := response.Meta["workspace"].(map[string]any)
	if !ok || workspaceMeta["root"] != workspace {
		t.Fatalf("expected workspace meta root, got %#v", response.Meta)
	}
	projectMeta, ok := response.Meta["project"].(map[string]any)
	if !ok {
		t.Fatalf("expected project meta, got %#v", response.Meta)
	}
	promptFiles, ok := projectMeta["promptFiles"].([]map[string]any)
	if !ok || !reflect.DeepEqual(promptFiles, []map[string]any{
		{"source": "workspace", "path": "AGENTS.md"},
		{"source": "agent", "path": "AGENTS.md"},
	}) {
		t.Fatalf("expected project prompt files, got %#v", projectMeta)
	}
	gitMeta, ok := projectMeta["git"].(map[string]any)
	if !ok || gitMeta["expectedBranch"] != "main" {
		t.Fatalf("expected project git meta, got %#v", projectMeta)
	}
	if _, ok := response.Meta["planningModeSupported"]; ok {
		t.Fatalf("did not expect planningModeSupported meta, got %#v", response.Meta)
	}
	if len(response.Controls) != 0 {
		t.Fatalf("expected no implicit planningMode control, got %#v", response.Controls)
	}
	response = s.buildAgentDetailResponse(catalog.AgentDefinition{
		Key:  "coder-custom",
		Name: "Coder Custom",
		Mode: catalog.AgentModeCoder,
		Controls: []map[string]any{
			{"key": "planningMode", "type": "custom"},
		},
	})
	if len(response.Controls) != 1 || response.Controls[0]["type"] != "custom" {
		t.Fatalf("expected explicit planningMode control to be preserved, got %#v", response.Controls)
	}
}

func TestAgentsEndpointReturnsCatalogFieldsAndScopeFiltering(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}, testFixtureOptions{
		setupRuntime: func(root string, cfg *config.Config) {
			workspace := filepath.Join(root, "workspace")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir workspace: %v", err)
			}
			for key, body := range map[string]string{
				"internal-agent": strings.Join([]string{
					"key: internal-agent",
					"name: Internal Agent",
					"mode: REACT",
					"visibility:",
					"  scopes:",
					"    - internal",
				}, "\n"),
				"invoke-agent": strings.Join([]string{
					"key: invoke-agent",
					"name: Invoke Agent",
					"mode: PROXY",
					"visibility:",
					"  scopes:",
					"    - invoke",
				}, "\n"),
				"coder-agent": strings.Join([]string{
					"key: coder-agent",
					"name: Coder Agent",
					"description: should stay out of summary json",
					"role: Code assistant",
					"mode: CODER",
					"modelConfig:",
					"  modelKey: agent-model",
					"stageSettings:",
					"  execute:",
					"    modelKey: execute-model",
					"    reasoningEffort: HIGH",
					"icon:",
					"  name: terminal",
					"  color: '#336699'",
					"runtimeConfig:",
					"  workspaceRoot: " + filepath.ToSlash(workspace),
					"kanban:",
					"  concurrency: 2",
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
	if _, _, err := fixture.chats.EnsureChat("chat-coder", "coder-agent", "", "coder chat"); err != nil {
		t.Fatalf("ensure coder chat: %v", err)
	}
	if err := fixture.chats.OnRunCompleted(chat.RunCompletion{ChatID: "chat-coder", RunID: "run-coder", UpdatedAtMillis: 1000}); err != nil {
		t.Fatalf("complete coder chat: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?includeChats=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode agents response: %v", err)
	}
	keys := make([]string, 0, len(response.Data))
	var coder api.AgentSummary
	for _, item := range response.Data {
		keys = append(keys, item.Key)
		if item.Key == "coder-agent" {
			coder = item
		}
	}
	if !containsString(keys, "internal-agent") || !containsString(keys, "invoke-agent") || !containsString(keys, "coder-agent") {
		t.Fatalf("default scope keys = %#v", keys)
	}
	if coder.Mode != catalog.AgentModeCoder || coder.WorkspaceDir == "" {
		t.Fatalf("coder summary = %#v", coder)
	}
	if coder.DefaultModelKey != "execute-model" || coder.DefaultReasoningEffort != "HIGH" {
		t.Fatalf("coder defaults = %#v", coder)
	}
	if coder.Role != "Code assistant" {
		t.Fatalf("coder role = %q, want Code assistant", coder.Role)
	}
	if len(coder.Chats) != 1 || coder.Chats[0].ChatID != "chat-coder" {
		t.Fatalf("coder chats = %#v", coder.Chats)
	}
	if !strings.Contains(rec.Body.String(), `"role":"Code assistant"`) {
		t.Fatalf("agents response should include role, got %s", rec.Body.String())
	}
	visibility, ok := coder.Meta["visibility"].(map[string]any)
	if !ok {
		t.Fatalf("coder summary should include visibility meta, got %#v", coder.Meta)
	}
	if !reflect.DeepEqual(visibility["scopes"], []any{"nav"}) {
		t.Fatalf("coder visibility scopes = %#v", visibility["scopes"])
	}
	if strings.Contains(rec.Body.String(), "should stay out") || strings.Contains(rec.Body.String(), `"description"`) || strings.Contains(rec.Body.String(), `"kanban"`) {
		t.Fatalf("agents response should omit backend fields, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=nav", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for nav scope, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode nav agents response: %v", err)
	}
	keys = keys[:0]
	for _, item := range response.Data {
		keys = append(keys, item.Key)
	}
	if containsString(keys, "internal-agent") || containsString(keys, "invoke-agent") || !containsString(keys, "coder-agent") {
		t.Fatalf("nav scope keys = %#v", keys)
	}
	for _, item := range response.Data {
		visibility, ok := item.Meta["visibility"].(map[string]any)
		if !ok {
			t.Fatalf("nav summary should include visibility meta, got %#v", item.Meta)
		}
		scopes, _ := visibility["scopes"].([]any)
		if !containsAnyString(scopes, "nav") {
			t.Fatalf("nav summary should only include nav-visible agents, got %s scopes %#v", item.Key, scopes)
		}
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=invoke", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for invoke scope, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode invoke agents response: %v", err)
	}
	keys = keys[:0]
	for _, item := range response.Data {
		keys = append(keys, item.Key)
	}
	if !containsString(keys, "invoke-agent") || containsString(keys, "mock-agent") || containsString(keys, "internal-agent") {
		t.Fatalf("invoke scope keys = %#v", keys)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=missing", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid scope, got %d: %s", rec.Code, rec.Body.String())
	}
}

func containsAnyString(values []any, needle string) bool {
	for _, value := range values {
		if s, ok := value.(string); ok && s == needle {
			return true
		}
	}
	return false
}

func TestAgentEndpointRequiresAgentKey(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestToolEndpointReturnsCanonicalJavaBuiltinSchemas(t *testing.T) {
	fixture := newMemoryEnabledTestFixture(t)

	for _, tc := range []struct {
		toolName         string
		requiredProperty string
	}{
		{toolName: "memory_read", requiredProperty: "sort"},
		{toolName: "datetime", requiredProperty: "timezone"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tool?toolName="+tc.toolName, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d: %s", tc.toolName, rec.Code, rec.Body.String())
		}

		var response api.ApiResponse[api.ToolDetailResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode tool response for %s: %v", tc.toolName, err)
		}
		if response.Data.Name != tc.toolName {
			t.Fatalf("expected tool %s, got %#v", tc.toolName, response.Data)
		}
		properties, _ := response.Data.Parameters["properties"].(map[string]any)
		if _, ok := properties[tc.requiredProperty]; !ok {
			t.Fatalf("expected property %s in %s schema, got %#v", tc.requiredProperty, tc.toolName, response.Data.Parameters)
		}
	}
}

func TestAgentEndpointRejectsBlankAgentKey(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=%20%20%20", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentEndpointReturnsNotFoundForUnknownAgent(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=missing-agent", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogEndpoints(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	for _, path := range []string{"/api/agents", "/api/agent?agentKey=mock-agent", "/api/teams", "/api/skills", "/api/tools", "/api/tool?toolName=bash"} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d", path, rec.Code)
		}
	}
}

func TestServerRejectsInvalidLocalJWTConfigAtStartup(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: filepath.Join(fixture.cfg.Paths.ChatsDir, "missing.pem"),
	}

	_, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err == nil {
		t.Fatal("expected startup auth config error")
	}
	if !strings.Contains(err.Error(), "load local jwt public key") {
		t.Fatalf("expected local key error, got %v", err)
	}
}

func TestQueryAcceptsValidLocalJWT(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "zenmind-local",
		"exp": float64(4102444800),
	}))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"content.delta"`) {
		t.Fatalf("expected streaming response, got %s", rec.Body.String())
	}
}

func TestQueryRejectsInvalidLocalJWT(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mustSignRS256JWT(t, privateKey, map[string]any{
		"sub": "tester",
		"iss": "wrong-issuer",
		"exp": float64(4102444800),
	}))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

func TestQueryRejectsMissingBearerWhenLocalJWTEnabled(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"鉴权测试"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"unauthorized"`) {
		t.Fatalf("expected unauthorized body, got %s", rec.Body.String())
	}
}

func TestExecuteInternalQueryBypassesHTTPAuth(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled:            true,
		LocalPublicKeyFile: publicKeyPath,
		Issuer:             "zenmind-local",
	}
	server := newServerFromFixture(t, fixture)

	status, body, err := server.ExecuteInternalQuery(context.Background(), api.QueryRequest{
		Message:  "计划任务内部执行",
		AgentKey: "mock-agent",
	})
	if err != nil {
		t.Fatalf("execute internal query: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if !strings.Contains(body, `"type":"content.delta"`) {
		t.Fatalf("expected streaming response, got %s", body)
	}
}
