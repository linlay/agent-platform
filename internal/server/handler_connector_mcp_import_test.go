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
)

func installInvalidConnectorFixture(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "tencent-map")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"connector.json":    `{"id":"tencent-map","name":"Map","version":"1.0.0","type":"mcp","authMode":"token"}`,
		"token-schema.json": `{"title_en":"Map","fields":[{"key":"TOKEN"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectorMCPImportIsolatesInvalidInstalledPackage(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	installInvalidConnectorFixture(t, f.cfg.Paths.EffectiveConnectorsCenterDir())
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(`{"mcpUrl":"http://127.0.0.1:8081/mcp"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid unrelated package blocked import: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connectors", nil))
	var response struct {
		Data struct {
			Connectors []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  string `json:"error"`
				HasMCP bool   `json:"hasMcp"`
			} `json:"connectors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("admin catalog: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	foundInvalid, foundImported := false, false
	for _, item := range response.Data.Connectors {
		if item.ID == "tencent-map" {
			foundInvalid = item.Status == "invalid" && strings.Contains(item.Error, `unknown field "title_en"`) && !item.HasMCP
		}
		if item.ID == "127.0.0.1" {
			foundImported = item.Status == "" && item.HasMCP
		}
	}
	if !foundInvalid || !foundImported {
		t.Fatalf("missing isolated diagnostic or imported package: %s", rec.Body.String())
	}
}

func TestConnectorMCPImportCreatesHTTPMCPPackage(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"mcpUrl":"https://mcp.example.test/mcp"}`)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"authMode":"mcp"`)) {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	dir := filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "mcp.example.test")
	mcpJSON, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || !bytes.Contains(mcpJSON, []byte("https://mcp.example.test/mcp")) || !bytes.Contains(mcpJSON, []byte("streamableHttp")) {
		t.Fatalf("generated mcp.json: %v %s", err, mcpJSON)
	}
	if _, err := os.Stat(filepath.Join(dir, "connector.json")); err != nil {
		t.Fatalf("generated connector.json: %v", err)
	}

	if rec := post(`{"mcpUrl":"http://localhost:8080/mcp"}`); rec.Code != http.StatusOK {
		t.Fatalf("localhost HTTP import: %d %s", rec.Code, rec.Body.String())
	}

	for _, body := range []string{
		`{"mcpUrl":"http://mcp.example.test/mcp"}`,
		`{"mcpUrl":"https://user:secret@mcp.example.test/mcp"}`,
		`{"mcpUrl":"https://mcp.example.test/mcp#fragment"}`,
		`{"mcpUrl":"ftp://mcp.example.test/mcp"}`,
		`{"mcpUrl":"https://builtin.example.test/mcp"}`,
	} {
		if rec := post(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	rec = post(`{"mcpUrl":"https://mcp.example.test/other"}`)
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("connector_exists")) {
		t.Fatalf("duplicate host: %d %s", rec.Code, rec.Body.String())
	}
	after, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || !bytes.Equal(after, mcpJSON) {
		t.Fatalf("duplicate host changed the installed package: %v %s", err, after)
	}
}

func TestConnectorMCPImportExplicitNoAuth(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(`{"mcpUrl":"http://private.example.test/mcp","authMode":"none"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authMode":"none"`) {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/connectors/connection?id=private.example.test", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"readiness":"no_auth"`) {
		t.Fatalf("no-auth readiness: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorMCPImportRejectsInvalidRequestsBeforePublication(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	reloader := &recordingServerCatalogReloader{}
	f.server.deps.CatalogReloader = reloader
	for _, body := range []string{
		`null`, `{}`, `{"mcpUrl":null}`,
		`{"MCPURL":"https://demo.example.test/mcp"}`,
		`{"mcpUrl":"https://demo.example.test/mcp","mcpUrl":"https://other.example.test/mcp"}`,
		`{"mcpUrl":"https://demo.example.test/mcp","overwrite":true}`,
		`{"mcpUrl":"https://demo.example.test/mcp","AuthMode":"none"}`,
		`{"mcpUrl":"https://demo.example.test/mcp","authMode":null}`,
		`{"mcpUrl":"https://demo.example.test/mcp","authMode":""}`,
		`{"mcpUrl":"https://demo.example.test/mcp","authMode":"token"}`,
		`{"mcpUrl":"https://demo.example.test/mcp#"}`,
		`{"mcpUrl":"https://demo.example.test/mcp"} {}`,
	} {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(reloader.reasons) != 0 {
		t.Fatalf("invalid requests caused reload: %v", reloader.reasons)
	}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(strings.Repeat(" ", connectorMCPImportMaxBytes+1))))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize request: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorMCPImportRollsBackFailedReload(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	f.server.deps.CatalogReloader = &recordingServerCatalogReloader{err: errServerCatalogReload}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(`{"mcpUrl":"https://rollback.example.test/mcp"}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("accepted failed reload: %s", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), "rollback.example.test")); !os.IsNotExist(err) {
		t.Fatalf("failed import retained published package: %v", err)
	}
}
