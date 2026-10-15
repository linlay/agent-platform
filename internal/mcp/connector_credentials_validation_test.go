package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestTokenValidationChecksAllMCPComponentsAndPreservesLiveCredentials(t *testing.T) {
	// TODO(connector-compat): 下个版本移除 legacy 参数及夹具分支，保留当前格式的验证覆盖。
	for _, legacy := range []bool{false, true} {
		name := "current manifest"
		if legacy {
			name = "legacy manifest"
		}
		t.Run(name, func(t *testing.T) { testTokenValidationChecksAllComponents(t, legacy) })
	}
}

func testTokenValidationChecksAllComponents(t *testing.T, legacy bool) {
	upstream := newSDKMCPTestServer(t, "read_document", nil)
	defer upstream.Close()
	var calls atomic.Int64
	var rejectSecond atomic.Bool
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != "correct-secret" || rejectSecond.Load() && r.URL.Path == "/two" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	files := map[string]any{
		"connector.json":    map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "token"},
		"token-schema.json": map[string]any{"fields": []map[string]any{{"key": "KEY", "label": "API Key", "type": "password", "required": true}}},
		"mcp.json":          map[string]any{"mcpServers": map[string]any{"one": map[string]any{"type": "streamableHttp", "url": endpoint.URL + "/one", "headers": map[string]any{"X-API-Key": "${KEY}"}}, "two": map[string]any{"type": "streamableHttp", "url": endpoint.URL + "/two", "headers": map[string]any{"X-API-Key": "${KEY}"}}}},
	}
	if legacy {
		files["connector.json"] = map[string]any{
			"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token",
			"token_schema": map[string]any{"fields": []map[string]any{{"name": "KEY", "label": "API Key", "type": "password", "required": true}}},
			"auth_bindings": map[string]any{
				"mcp:one": map[string]any{"headers": map[string]any{"X-API-Key": "${KEY}"}},
				"mcp:two": map[string]any{"headers": map[string]any{"X-API-Key": "${KEY}"}},
			},
		}
		files["mcp.json"] = map[string]any{"mcpServers": map[string]any{
			"one": map[string]any{"type": "http", "url": endpoint.URL + "/one"},
			"two": map[string]any{"type": "http", "url": endpoint.URL + "/two"},
		}}
		delete(files, "token-schema.json")
	}
	for name, value := range files {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	publicRegistry, err := NewRegistryWithSources(connector.Sources{ExternalRoot: sources.ExternalRoot, StateRoot: sources.StateRoot})
	if err != nil {
		t.Fatal(err)
	}
	probe := NewClientWithGate(publicRegistry, endpoint.Client(), NewAvailabilityGate())
	defer probe.Close()
	manager := connectorauth.New(t.Context(), sources, nil).WithCredentialValidator(probe.ValidateConnectorCredentials)
	if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"KEY": "correct-secret"}); err != nil {
		t.Fatal(err)
	}
	pkg, _ := sources.Load("demo")
	pkg.SetConfigured(true)
	before := calls.Load()
	if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"KEY": "wrong-secret"}); !errors.Is(err, connectorauth.ErrTokenRejected) {
		t.Fatalf("wrong token accepted: %v", err)
	}
	if calls.Load() <= before {
		t.Fatal("validation never reached real MCP server")
	}
	rejectSecond.Store(true)
	if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"KEY": "correct-secret"}); !errors.Is(err, connectorauth.ErrTokenRejected) {
		t.Fatalf("skipped second required component: %v", err)
	}
	values, ready, err := connectorauth.TokenValues(pkg)
	if err != nil || !ready || values["KEY"] != "correct-secret" {
		t.Fatal("invalid update changed live credentials", err)
	}
	state, err := pkg.ReadConnection()
	if err != nil || !state.Configured {
		t.Fatal("invalid update changed preferences", state, err)
	}
	if len(probe.slots) != 0 {
		t.Fatal("validation changed global sessions")
	}
	for _, server := range publicRegistry.Servers() {
		if server.Headers["X-API-Key"] != "${KEY}" {
			t.Fatal("candidate token reached global catalog")
		}
	}
}
