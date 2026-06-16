package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
)

func setupAdminRegistriesFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			mcpDir := filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers")
			viewportDir := filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers")
			if err := os.MkdirAll(mcpDir, 0o755); err != nil {
				t.Fatalf("mkdir mcp dir: %v", err)
			}
			if err := os.MkdirAll(viewportDir, 0o755); err != nil {
				t.Fatalf("mkdir viewport dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(mcpDir, "invalid-yaml.yml"), []byte("serverKey: broken\n  baseUrl: bad\n"), 0o644); err != nil {
				t.Fatalf("write invalid mcp registry: %v", err)
			}
			if err := os.WriteFile(filepath.Join(viewportDir, "missing-base.yml"), []byte("serverKey: missing-base\nendpointPath: /mcp\n"), 0o644); err != nil {
				t.Fatalf("write invalid viewport registry: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "unknown-provider.yml"), []byte(strings.Join([]string{
				"key: unknown-provider-model",
				"name: Unknown Provider Model",
				"provider: missing-provider",
				"protocol: OPENAI",
				"modelId: unknown-provider-model-id",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write unknown provider model: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.RegistriesDir, "models", "acp-passthrough.yml"), []byte(strings.Join([]string{
				"key: acp-passthrough",
				"name: ACP Passthrough",
				"protocol: ACP_PASSTHROUGH",
				"modelId: gpt-5-codex",
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write acp passthrough model: %v", err)
			}
		},
	})
}

func TestAdminRegistriesEndpointIncludesInvalidFiles(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/registries", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin registries status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.AdminRegistryListResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode registries response: %v", err)
	}
	byFile := map[string]api.AdminRegistrySummary{}
	for _, item := range resp.Data.Items {
		byFile[item.Category+"/"+item.File] = item
	}
	if byFile["providers/mock.yml"].Status != "ready" {
		t.Fatalf("mock provider should be ready: %#v", byFile["providers/mock.yml"])
	}
	if item := byFile["mcp-servers/invalid-yaml.yml"]; item.Status != "invalid" || len(item.Diagnostics) == 0 || item.Diagnostics[0].Code != "invalid_yaml" {
		t.Fatalf("invalid yaml diagnostics missing: %#v", item)
	}
	if item := byFile["models/unknown-provider.yml"]; item.Status != "invalid" || len(item.Diagnostics) == 0 || item.Diagnostics[0].Code != "unknown_provider" {
		t.Fatalf("unknown provider diagnostics missing: %#v", item)
	}
	if item := byFile["models/acp-passthrough.yml"]; item.Status != "ready" || len(item.Diagnostics) != 0 {
		t.Fatalf("acp passthrough providerless model should be ready: %#v", item)
	}
	if item := byFile["viewport-servers/missing-base.yml"]; item.Status != "invalid" || len(item.Diagnostics) == 0 || item.Diagnostics[0].Code != "missing_base_url" {
		t.Fatalf("viewport diagnostics missing: %#v", item)
	}
}

func TestAdminRegistryDetailSaveValidateAndPathGuard(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/registries/detail?category=providers&file=mock.yml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detailResp api.ApiResponse[api.AdminRegistryDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if !strings.Contains(detailResp.Data.Content, "apiKey: test-key") {
		t.Fatalf("detail should expose YAML source, got %q", detailResp.Data.Content)
	}
	if _, leaked := detailResp.Data.Summary["apiKey"]; leaked {
		t.Fatalf("summary should not expose apiKey: %#v", detailResp.Data.Summary)
	}

	invalidBody := bytes.NewBufferString(`{"category":"providers","file":"bad.yml","content":"key: bad\n  baseUrl: nope\n"}`)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/registries/detail", invalidBody))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid YAML save should fail, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.RegistriesDir, "providers", "bad.yml")); !os.IsNotExist(err) {
		t.Fatalf("invalid YAML should not be written, stat err=%v", err)
	}

	validBody := bytes.NewBufferString(`{"category":"providers","file":"new-provider.yml","content":"key: new-provider\nbaseUrl: http://localhost:19999\ndefaultModel: new-model\n"}`)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/registries/detail", validBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid save status = %d body=%s", rec.Code, rec.Body.String())
	}
	var savedResp api.ApiResponse[api.AdminRegistryDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &savedResp); err != nil {
		t.Fatalf("decode save response: %v", err)
	}
	if savedResp.Data.Status != "ready" || savedResp.Data.Key != "new-provider" {
		t.Fatalf("unexpected save response: %#v", savedResp.Data)
	}
	data, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.RegistriesDir, "providers", "new-provider.yml"))
	if err != nil || !strings.Contains(string(data), "key: new-provider") {
		t.Fatalf("saved file missing: %v content=%s", err, string(data))
	}

	validateBody := bytes.NewBufferString(`{"category":"models","file":"draft.yml","content":"key: draft\nprovider: missing\nmodelId: draft\n"}`)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/registries/validate", validateBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var validateResp api.ApiResponse[api.AdminRegistryValidateResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &validateResp); err != nil {
		t.Fatalf("decode validate response: %v", err)
	}
	if validateResp.Data.Status != "invalid" || len(validateResp.Data.Diagnostics) == 0 || validateResp.Data.Diagnostics[0].Code != "unknown_provider" {
		t.Fatalf("expected invalid validate diagnostics, got %#v", validateResp.Data)
	}

	validateBody = bytes.NewBufferString(`{"category":"models","file":"draft-acp.yml","content":"key: draft-acp\nprotocol: ACP_PASSTHROUGH\nmodelId: gpt-5-codex\n"}`)
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/registries/validate", validateBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("validate acp status = %d body=%s", rec.Code, rec.Body.String())
	}
	validateResp = api.ApiResponse[api.AdminRegistryValidateResponse]{}
	if err := json.Unmarshal(rec.Body.Bytes(), &validateResp); err != nil {
		t.Fatalf("decode acp validate response: %v", err)
	}
	if validateResp.Data.Status != "ready" || len(validateResp.Data.Diagnostics) != 0 {
		t.Fatalf("expected ready acp validate response, got %#v", validateResp.Data)
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/registries/detail?category=providers&file=../mock.yml", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal should fail, got %d body=%s", rec.Code, rec.Body.String())
	}
}
