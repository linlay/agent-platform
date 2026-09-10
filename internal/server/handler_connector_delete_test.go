package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
)

func deleteConnectorRequest(s *Server, id string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/connectors/detail?id="+id, nil))
	return rec
}

func TestConnectorDeleteHTTPContractAndRollback(t *testing.T) {
	f := agentConnectorsFixture(t)
	root := f.server.connectorSources().ExternalRoot
	for id, status := range map[string]int{"builtin.dbx": 403, "missing": 404, "../bad": 400, "": 400, "docs": 409} {
		rec := deleteConnectorRequest(f.server, id)
		if rec.Code != status {
			t.Fatalf("%s: %d %s", id, rec.Code, rec.Body.String())
		}
		if status == 409 && !strings.Contains(rec.Body.String(), `"agentKeys":["mock-agent"]`) {
			t.Fatal(rec.Body.String())
		}
	}
	reloader := &recordingServerCatalogReloader{err: errServerCatalogReload}
	f.server.deps.CatalogReloader = reloader
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 500 {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "mail", "connector.json")); err != nil {
		t.Fatal("rollback lost package", err)
	}
	reloader.err = nil
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 404 {
		t.Fatalf("repeat: %d", rec.Code)
	}
}

func TestConnectorDeleteChecksFreshInvalidSourcesAndLeasedRuntimes(t *testing.T) {
	f := agentConnectorsFixture(t)
	registry := f.registry.(*catalog.FileRegistry)
	path := filepath.Join(f.server.deps.Config.Paths.AgentsDir, "new-invalid.yml")
	// The Agent is not in the loaded catalog and lacks required model settings.
	if err := os.WriteFile(path, []byte("key: invalid\nconnectorConfig:\n  connectors:\n    - mail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := deleteConnectorRequest(f.server, "mail"); rec.Code != 409 || !strings.Contains(rec.Body.String(), "invalid") {
		t.Fatalf("fresh source: %d %s", rec.Code, rec.Body.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	registry.SetRuntimeReload(func() {
		if err := f.server.reloadAgentCatalog(context.Background()); err != nil {
			t.Error(err)
		}
	})
	_, release, ok := registry.AcquireAgentRuntime("mock-agent")
	if !ok {
		t.Fatal("missing Agent")
	}
	t.Cleanup(release)
	response := agentConnectorResponse(t, agentConnectorRequest(f.server, "PUT", "", map[string]any{"agentKey": "mock-agent", "connectorId": "docs", "enabled": false}))
	if !response.ReloadPending {
		t.Fatal("expected pending runtime reload")
	}
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 409 {
		t.Fatalf("leased: %d %s", rec.Code, rec.Body.String())
	}
	release()
	if rec := deleteConnectorRequest(f.server, "docs"); rec.Code != 200 {
		t.Fatalf("released: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.server.connectorSources().Load("docs"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package still installed: %v", err)
	}
}
