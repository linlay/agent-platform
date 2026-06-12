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

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/order", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime order read status = %d body=%s", rec.Code, rec.Body.String())
	}
	var runtimeRead api.ApiResponse[api.AgentOrderResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &runtimeRead); err != nil {
		t.Fatalf("decode runtime order read: %v", err)
	}
	if !reflect.DeepEqual(runtimeRead.Data.Order, []string{"mock-agent"}) {
		t.Fatalf("runtime order should filter invalid keys, got %#v", runtimeRead.Data.Order)
	}
}
