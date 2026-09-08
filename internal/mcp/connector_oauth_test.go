package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"golang.org/x/oauth2"
)

func TestConnectorOAuthToolCallsUseCurrentCredentials(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "read_document", nil)
	defer upstream.Close()
	var received atomic.Int32
	var expected atomic.Value
	expected.Store("first")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		received.Add(1)
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	os.MkdirAll(dir, 0o755)
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "oauth", "oauth": map[string]any{"discovery": true}},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": server.URL}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Server("demo")
	if definition.SetupError == "" {
		t.Fatal("missing credentials did not require setup")
	}
	state, err := connectorauth.StateDir(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(state, 0o700)
	save := func(value string) {
		data, _ := json.Marshal(map[string]any{"resource": server.URL, "config": oauth2.Config{}, "token": &oauth2.Token{AccessToken: value, TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}})
		if err := os.WriteFile(filepath.Join(state, "oauth.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	save("first")
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	definition, _ = registry.Server("demo")
	if definition.SetupError != "" {
		t.Fatal(definition.SetupError)
	}
	client := NewClientWithGate(registry, server.Client(), nil)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := client.ListTools(ctx, "demo")
	if err != nil || len(tools) != 1 || tools[0].Name != "read_document" {
		t.Fatalf("discovery: %v, %v", tools, err)
	}
	expected.Store("second")
	save("second")
	if _, err := client.CallTool(ctx, "demo", "read_document", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	manager := connectorauth.New(ctx, connector.Sources{ExternalRoot: root}, nil)
	if err := manager.Logout(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	before := received.Load()
	if _, err := client.CallTool(ctx, "demo", "read_document", map[string]any{}, nil); err == nil {
		t.Fatal("old MCP session survived logout")
	}
	if received.Load() != before {
		t.Fatal("request was sent after logout")
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	definition, _ = registry.Server("demo")
	if definition.SetupError == "" {
		t.Fatal("logout was not reflected by registry")
	}
}
