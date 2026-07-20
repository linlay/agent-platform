package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
)

func setupAdminAgentsFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			semanticDir := filepath.Join(cfg.Paths.AgentsDir, "invalid-semantic")
			if err := os.MkdirAll(semanticDir, 0o755); err != nil {
				t.Fatalf("mkdir invalid semantic agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(semanticDir, "agent.yml"), []byte(strings.Join([]string{
				"key: invalid-semantic",
				"name: Invalid Semantic",
				"mode: REACT",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: relative/path",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write invalid semantic agent: %v", err)
			}

			yamlDir := filepath.Join(cfg.Paths.AgentsDir, "invalid-yaml")
			if err := os.MkdirAll(yamlDir, 0o755); err != nil {
				t.Fatalf("mkdir invalid yaml agent: %v", err)
			}
			if err := os.WriteFile(filepath.Join(yamlDir, "agent.yml"), []byte("key: invalid-yaml\n  name: bad-indent\n"), 0o644); err != nil {
				t.Fatalf("write invalid yaml agent: %v", err)
			}
		},
	})
}

func TestAdminAgentsEndpointIncludesInvalidAgentsAndRuntimeEndpointDoesNot(t *testing.T) {
	fixture := setupAdminAgentsFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin agents status = %d body=%s", rec.Code, rec.Body.String())
	}
	var adminResp api.ApiResponse[[]api.AdminAgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &adminResp); err != nil {
		t.Fatalf("decode admin agents response: %v", err)
	}
	adminByKey := map[string]api.AdminAgentSummary{}
	for _, item := range adminResp.Data {
		adminByKey[item.Key] = item
	}
	for _, key := range []string{"mock-agent", "invalid-semantic", "invalid-yaml"} {
		if _, ok := adminByKey[key]; !ok {
			t.Fatalf("admin agents missing %s: %#v", key, adminResp.Data)
		}
	}
	if adminByKey["mock-agent"].Status != "ready" {
		t.Fatalf("mock-agent status = %q", adminByKey["mock-agent"].Status)
	}
	if adminByKey["invalid-semantic"].Status != "invalid" || len(adminByKey["invalid-semantic"].Diagnostics) == 0 {
		t.Fatalf("invalid semantic diagnostics missing: %#v", adminByKey["invalid-semantic"])
	}
	if adminByKey["invalid-yaml"].Status != "invalid" || adminByKey["invalid-yaml"].Diagnostics[0].Code != "invalid_yaml" {
		t.Fatalf("invalid yaml diagnostics unexpected: %#v", adminByKey["invalid-yaml"])
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents?scope=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime agents status = %d body=%s", rec.Code, rec.Body.String())
	}
	var agentsResp api.ApiResponse[[]api.AgentSummary]
	if err := json.Unmarshal(rec.Body.Bytes(), &agentsResp); err != nil {
		t.Fatalf("decode runtime agents response: %v", err)
	}
	for _, item := range agentsResp.Data {
		if item.Key == "invalid-semantic" || item.Key == "invalid-yaml" {
			t.Fatalf("runtime agents should not include invalid item: %#v", agentsResp.Data)
		}
	}
	if _, ok := fixture.registry.AgentDefinition("invalid-semantic"); ok {
		t.Fatal("invalid semantic agent should not be in runtime registry")
	}
}

func TestAdminAgentDetailKeepsEditableFieldsForReadyAgents(t *testing.T) {
	fixture := newTestFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/detail?agentKey=mock-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin ready detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detailResp api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("decode admin ready detail response: %v", err)
	}
	detail := detailResp.Data
	if detail.Status != "ready" {
		t.Fatalf("expected ready detail status, got %#v", detail.Status)
	}
	if detail.Definition["key"] != "mock-agent" {
		t.Fatalf("expected editable definition to be returned, got %#v", detail.Definition)
	}
	if detail.Source == nil || !strings.HasSuffix(detail.Source.Path, filepath.Join("mock-agent", "agent.yml")) {
		t.Fatalf("expected editable source path, got %#v", detail.Source)
	}
}

func TestAdminAgentSourceReadsRawYAMLAndReloadsSavedSource(t *testing.T) {
	fixture := newTestFixture(t)

	read := httptest.NewRecorder()
	fixture.server.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/admin/agents/source?agentKey=mock-agent", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("read source status = %d body=%s", read.Code, read.Body.String())
	}
	var sourceResp api.ApiResponse[api.AdminAgentSourceResponse]
	if err := json.Unmarshal(read.Body.Bytes(), &sourceResp); err != nil {
		t.Fatalf("decode source response: %v", err)
	}
	if sourceResp.Data.Source.Path == "" || sourceResp.Data.SHA256 == "" || sourceResp.Data.Encoding != "utf-8" {
		t.Fatalf("source metadata missing: %#v", sourceResp.Data)
	}
	if !strings.Contains(sourceResp.Data.Content, "name: Mock Agent") {
		t.Fatalf("expected raw agent YAML, got:\n%s", sourceResp.Data.Content)
	}

	updatedContent := "# retained source comment\n" + strings.Replace(sourceResp.Data.Content, "name: Mock Agent", "name: Raw Source Agent", 1)
	payload, err := json.Marshal(api.UpdateAdminAgentSourceRequest{
		Key:        "mock-agent",
		Content:    updatedContent,
		BaseSHA256: sourceResp.Data.SHA256,
	})
	if err != nil {
		t.Fatalf("marshal source update: %v", err)
	}
	saved := httptest.NewRecorder()
	fixture.server.ServeHTTP(saved, httptest.NewRequest(http.MethodPut, "/api/admin/agents/source", bytes.NewReader(payload)))
	if saved.Code != http.StatusOK {
		t.Fatalf("save source status = %d body=%s", saved.Code, saved.Body.String())
	}
	var savedResp api.ApiResponse[api.AdminAgentSourceResponse]
	if err := json.Unmarshal(saved.Body.Bytes(), &savedResp); err != nil {
		t.Fatalf("decode saved source response: %v", err)
	}
	if savedResp.Data.Content != updatedContent || savedResp.Data.Detail.Name != "Raw Source Agent" || savedResp.Data.Detail.Status != "ready" {
		t.Fatalf("unexpected saved source response: %#v", savedResp.Data)
	}
	onDisk, err := os.ReadFile(savedResp.Data.Source.Path)
	if err != nil {
		t.Fatalf("read saved source: %v", err)
	}
	if string(onDisk) != updatedContent {
		t.Fatalf("source file was re-rendered instead of preserved:\n%s", onDisk)
	}

	detail := httptest.NewRecorder()
	fixture.server.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/admin/agents/detail?agentKey=mock-agent", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "Raw Source Agent") {
		t.Fatalf("reloaded agent detail = %d body=%s", detail.Code, detail.Body.String())
	}
}

func TestAdminAgentSourceRejectsInvalidEditsAndStaleWrites(t *testing.T) {
	fixture := setupAdminAgentsFixture(t)

	invalidRead := httptest.NewRecorder()
	fixture.server.ServeHTTP(invalidRead, httptest.NewRequest(http.MethodGet, "/api/admin/agents/source?agentKey=invalid-yaml", nil))
	if invalidRead.Code != http.StatusOK || !strings.Contains(invalidRead.Body.String(), "bad-indent") {
		t.Fatalf("invalid source must remain readable: %d body=%s", invalidRead.Code, invalidRead.Body.String())
	}

	read := httptest.NewRecorder()
	fixture.server.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/admin/agents/source?agentKey=mock-agent", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("read source status = %d body=%s", read.Code, read.Body.String())
	}
	var sourceResp api.ApiResponse[api.AdminAgentSourceResponse]
	if err := json.Unmarshal(read.Body.Bytes(), &sourceResp); err != nil {
		t.Fatalf("decode source response: %v", err)
	}
	original := sourceResp.Data.Content
	path := sourceResp.Data.Source.Path

	assertRejected := func(name, content, baseSHA256 string, wantStatus int) {
		t.Helper()
		payload, err := json.Marshal(api.UpdateAdminAgentSourceRequest{
			Key: "mock-agent", Content: content, BaseSHA256: baseSHA256,
		})
		if err != nil {
			t.Fatalf("%s marshal payload: %v", name, err)
		}
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/agents/source", bytes.NewReader(payload)))
		if rec.Code != wantStatus {
			t.Fatalf("%s status = %d body=%s", name, rec.Code, rec.Body.String())
		}
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s read source: %v", name, err)
		}
		if string(onDisk) != original {
			t.Fatalf("%s changed source on rejected save", name)
		}
	}

	assertRejected("invalid yaml", "key: mock-agent\n  name: invalid\n", sourceResp.Data.SHA256, http.StatusBadRequest)
	assertRejected("key mismatch", strings.Replace(original, "key: mock-agent", "key: another-agent", 1), sourceResp.Data.SHA256, http.StatusBadRequest)
	assertRejected("semantic error", strings.Replace(original, "mode: REACT", "mode: TEAM", 1), sourceResp.Data.SHA256, http.StatusBadRequest)
	assertRejected("stale hash", original, "stale-hash", http.StatusConflict)

	pathAttempt := httptest.NewRecorder()
	fixture.server.ServeHTTP(pathAttempt, httptest.NewRequest(http.MethodGet, "/api/admin/agents/source?agentKey=../../etc/passwd", nil))
	if pathAttempt.Code != http.StatusBadRequest {
		t.Fatalf("unregistered source path status = %d body=%s", pathAttempt.Code, pathAttempt.Body.String())
	}
}

func TestAdminAgentDetailReturnsDiagnosticsForInvalidAgents(t *testing.T) {
	fixture := setupAdminAgentsFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/detail?agentKey=invalid-semantic", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detailResp api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("decode admin detail response: %v", err)
	}
	detail := detailResp.Data
	if detail.Status != "invalid" || len(detail.Diagnostics) == 0 || detail.Diagnostics[0].Code != "invalid_config" {
		t.Fatalf("unexpected invalid detail diagnostics: %#v", detail)
	}
	if detail.Source == nil || !strings.HasSuffix(detail.Source.Path, filepath.Join("invalid-semantic", "agent.yml")) {
		t.Fatalf("expected invalid detail source path, got %#v", detail.Source)
	}
	if detail.Definition["key"] != "invalid-semantic" {
		t.Fatalf("expected YAML map definition to be returned, got %#v", detail.Definition)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agent?agentKey=invalid-semantic", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("runtime detail should not expose invalid agent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminAgentOrderAllowsInvalidKeysAndRuntimeOrderFiltersThem(t *testing.T) {
	fixture := setupAdminAgentsFixture(t)

	body := bytes.NewBufferString(`{"order":["invalid-semantic","mock-agent"]}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/agents/order", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin order write status = %d body=%s", rec.Code, rec.Body.String())
	}
	var writeResp api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &writeResp); err != nil {
		t.Fatalf("decode admin order write: %v", err)
	}
	if !reflect.DeepEqual(writeResp.Data.Order, []string{"invalid-semantic", "mock-agent"}) {
		t.Fatalf("unexpected admin write order: %#v", writeResp.Data.Order)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin order read status = %d body=%s", rec.Code, rec.Body.String())
	}
	var adminRead api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &adminRead); err != nil {
		t.Fatalf("decode admin order read: %v", err)
	}
	if !reflect.DeepEqual(adminRead.Data.Order, []string{"invalid-semantic", "mock-agent"}) {
		t.Fatalf("unexpected admin read order: %#v", adminRead.Data.Order)
	}

}

func TestAdminAgentOrderOmitsAbsentUpdatedAt(t *testing.T) {
	fixture := setupAdminAgentsFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh admin order status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode fresh admin order: %v", err)
	}
	if response.Data.UpdatedAt != nil {
		t.Fatalf("missing order file must omit updatedAt, got %#v", response.Data)
	}
}
