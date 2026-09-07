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

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func writeMCPConnectorForTest(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]any{
		"connector.json": connector.Manifest{ID: id, Name: id, Version: "1.0.0", Type: "mcp", AuthMode: "none"},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectorDefinitionSaveConflictAndRollback(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	root := fixture.server.deps.Config.Paths.EffectiveConnectorsDir()
	writeMCPConnectorForTest(t, root, "demo")
	previous, err := connector.ReadFile(root, "demo", "mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	reloader := &recordingServerCatalogReloader{}
	fixture.server.deps.CatalogReloader = reloader
	content := `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://new.example.test/mcp"}}}`
	save := func(base string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"id": "demo", "file": "mcp.json", "content": content, "baseSha256": base})
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/connectors/detail", bytes.NewReader(body)))
		return rec
	}
	if rec := save("stale"); rec.Code == http.StatusOK {
		t.Fatal("accepted stale update")
	}
	if rec := save(previous.SHA256); rec.Code != http.StatusOK {
		t.Fatalf("save failed: %d %s", rec.Code, rec.Body.String())
	}
	if len(reloader.reasons) != 1 || reloader.reasons[0] != "connectors" {
		t.Fatalf("reloads %v", reloader.reasons)
	}
	current, _ := connector.ReadFile(root, "demo", "mcp.json")
	reloader.err = errServerCatalogReload
	content = `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://failed.example.test/mcp"}}}`
	if rec := save(current.SHA256); rec.Code == http.StatusOK {
		t.Fatal("accepted failed reload")
	}
	if restored, _ := connector.ReadFile(root, "demo", "mcp.json"); restored.SHA256 != current.SHA256 {
		t.Fatal("failed reload changed definition")
	}
}

func TestConnectorSkillsExcludedFromMustUseCatalog(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		cfg.Paths.BuiltinConnectorsDir = t.TempDir()
		if err := connector.WriteBuiltin(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx"), "dbx", "1.0.0", "darwin"); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("\nconnectorConfig:\n  connectors:\n    - builtin.dbx\n")...)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}})
	result, err := fixture.server.listSkillsForAgent("mock-agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range result.Skills {
		if strings.HasPrefix(skill.Key, "connector-") {
			t.Fatal("connector skill is selectable")
		}
	}
}

func TestBuiltinConnectorAPIListsReadsAndRejectsMutation(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	root := t.TempDir()
	fixture.server.deps.Config.Paths.BuiltinConnectorsDir = root
	if err := connector.WriteBuiltin(filepath.Join(root, "builtin.httpx"), "httpx", "0.1.8", "darwin"); err != nil {
		t.Fatal(err)
	}
	external := fixture.server.deps.Config.Paths.EffectiveConnectorsDir()
	writeMCPConnectorForTest(t, external, "remote")
	for _, endpoint := range []string{"/api/connectors", "/api/admin/connectors"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
		var response struct {
			Data struct {
				Connectors []connector.Summary `json:"connectors"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || len(response.Data.Connectors) != 2 {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		item := response.Data.Connectors[0]
		if item.ID != "builtin.httpx" || !item.Builtin || !item.ReadOnly || item.CanDelete || len(item.Skills) != 1 || item.Skills[0] != "builtin-httpx" {
			t.Fatalf("builtin metadata: %#v", item)
		}
	}
	target := "/api/admin/connectors/detail?id=builtin.httpx&file=connector.json"
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("read: %s", rec.Body.String())
	}
	previous, err := connector.ReadFile(root, "builtin.httpx", "connector.json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"id": "builtin.httpx", "file": "connector.json", "content": previous.Content, "baseSha256": previous.SHA256})
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(method, target, bytes.NewReader(body)))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cannot be modified or deleted") {
			t.Fatalf("%s: %d %s", method, rec.Code, rec.Body.String())
		}
	}
	current, err := connector.ReadFile(root, "builtin.httpx", "connector.json")
	if err != nil || current.SHA256 != previous.SHA256 {
		t.Fatalf("builtin changed: %v", err)
	}
}
