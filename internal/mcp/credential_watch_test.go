package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestHTTPBindingAndCredentialWatcherKeepPackageUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "configured-token" {
			t.Error("binding was not applied")
		}
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "docx")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "docx", "name": "DocX", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "required": true}}}, "auth_bindings": map[string]any{"mcp:DocX": map[string]any{"headers": map[string]string{"X-API-Key": "${API_KEY}"}}}},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"DocX": map[string]any{"type": "http", "url": upstream.URL, "disabled": false}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources := connector.Sources{ExternalRoot: root}
	registry, err := NewRegistryWithSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	server, ok := registry.Server("docx.docx")
	if !ok || server.SetupError == "" {
		t.Fatal("expected unconfigured component")
	}
	original, _ := os.ReadFile(filepath.Join(dir, "mcp.json"))
	manifestInfo, _ := os.Stat(filepath.Join(dir, "connector.json"))
	reloader := NewRegistryReloader(registry, nil)
	reloader.WatchCredentials(t.Context())
	manager := connectorauth.New(t.Context(), sources, nil).WithCredentialValidator(func(context.Context, connector.Package, map[string]string) error { return nil }).ForOwner("alice")
	if _, err := manager.SetToken(t.Context(), "docx", map[string]string{"API_KEY": "configured-token"}); err != nil {
		t.Fatal(err)
	}
	// Global catalog remains ownerless even after private credentials change.
	if err := reloader.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	server, _ = registry.Server("docx.docx")
	if server.SetupError == "" {
		t.Fatal("private credentials entered global catalog")
	}
	pkg, err := sources.Load("docx")
	if err != nil {
		t.Fatal(err)
	}
	pkg.Owner = "alice"
	server, err = connectorServer(pkg, "DocX")
	if err != nil || server.SetupError != "" {
		t.Fatal("owner credentials unavailable", err)
	}
	if server.Headers["X-API-Key"] != "${API_KEY}" {
		t.Fatal("secret in registry")
	}
	client := (&Client{httpClient: upstream.Client()}).httpClientForServer(server)
	response, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	after, _ := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if string(after) != string(original) {
		t.Fatal("MCP file rewritten")
	}
	newInfo, _ := os.Stat(filepath.Join(dir, "connector.json"))
	if !newInfo.ModTime().Equal(manifestInfo.ModTime()) {
		t.Fatal("manifest touched")
	}
}
