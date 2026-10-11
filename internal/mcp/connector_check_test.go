package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/connector"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func connectorCheckFixture(t *testing.T, endpoint, mode string) (connector.Sources, connector.Package) {
	t.Helper()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": mode},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "http", "url": endpoint}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return sources, pkg
}

func TestConnectorCheckWithoutAgentMountUsesSDKAndClosesSession(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "read_document", nil)
	defer upstream.Close()
	var calls, closed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			closed.Add(1)
		}
		if r.Method == http.MethodPost {
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Error(err)
			}
			if body["method"] == "tools/call" {
				calls.Add(1)
			}
			r.Body = io.NopCloser(strings.NewReader(string(data)))
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	sources, pkg := connectorCheckFixture(t, server.URL, "no_auth")
	result, err := NewConnectorChecker(sources).CheckConnector(t.Context(), "demo", "")
	if err != nil || result.Status != "passed" || result.Initialize.Status != "passed" || result.ToolDiscovery.Status != "passed" || result.ToolDiscovery.Count != 1 || result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("check: %+v, %v", result, err)
	}
	if result.ToolDiscovery.Tools[0].Name != "read_document" || calls.Load() != 0 || closed.Load() == 0 {
		t.Fatalf("business call or leaked session: %+v, calls=%d closed=%d", result, calls.Load(), closed.Load())
	}
	state, _ := pkg.ConnectorStateDir()
	if _, err := os.Stat(filepath.Join(state, "connection.json")); !os.IsNotExist(err) {
		t.Fatalf("check changed connector configuration: %v", err)
	}
}

func TestConnectorCheckOAuthUsesStoredCredentialsAndRequiresLogin(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "read_document", nil)
	defer upstream.Close()
	var requests atomic.Int32
	var reject atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if reject.Load() || r.Header.Get("Authorization") != "Bearer private-check-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	sources, pkg := connectorCheckFixture(t, server.URL, "mcp")
	checker := NewConnectorChecker(sources)
	result, err := checker.CheckConnector(t.Context(), "demo", "main")
	if err != nil || result.Status != "authorization_required" || result.Package.Status != "passed" || result.Initialize.Status != "not_run" || requests.Load() != 0 {
		t.Fatalf("missing credentials: %+v, %v", result, err)
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	state, _ := pkg.ConnectorStateDir()
	data, _ := json.Marshal(map[string]any{"resource": server.URL, "config": oauth2.Config{}, "token": &oauth2.Token{AccessToken: "private-check-token", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}})
	if err := os.WriteFile(filepath.Join(state, "oauth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = checker.CheckConnector(t.Context(), "demo", "main")
	if err != nil || result.Status != "passed" || requests.Load() == 0 {
		t.Fatalf("authorized probe: %+v, %v", result, err)
	}
	output, _ := json.Marshal(result)
	if strings.Contains(string(output), "private-check-token") || strings.Contains(string(output), server.URL) {
		t.Fatalf("sensitive probe output: %s", output)
	}
	reject.Store(true)
	result, err = checker.CheckConnector(t.Context(), "demo", "main")
	if err != nil || result.Status != "authorization_required" || result.Initialize.Code != "authorization_required" || result.ToolDiscovery.Status != "not_run" {
		t.Fatalf("rejected stored grant: %+v, %v", result, err)
	}
}

func TestConnectorCheckFailuresAreBoundedAndSanitized(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "private-remote-error", status)
			}))
			defer server.Close()
			sources, _ := connectorCheckFixture(t, server.URL, "no_auth")
			result, err := NewConnectorChecker(sources).CheckConnector(t.Context(), "demo", "main")
			output, _ := json.Marshal(result)
			if err != nil || result.Status != "failed" || result.Initialize.Status != "failed" || strings.Contains(string(output), "private-") || strings.Contains(string(output), server.URL) {
				t.Fatalf("failed check: %s, %v", output, err)
			}
			if status == http.StatusUnauthorized && result.Initialize.Code != "authentication_mismatch" {
				t.Fatalf("anonymous rejection misreported: %+v", result)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()
	sources, _ := connectorCheckFixture(t, server.URL, "no_auth")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	result, err := NewConnectorChecker(sources).CheckConnector(ctx, "demo", "main")
	if err != nil || result.Initialize.Code != "timeout" {
		t.Fatalf("timeout: %+v, %v", result, err)
	}
}

func TestConnectorCheckRejectsInvalidOrUnsupportedComponents(t *testing.T) {
	sources, pkg := connectorCheckFixture(t, "https://example.test/mcp", "no_auth")
	checker := NewConnectorChecker(sources)
	result, _ := checker.CheckConnector(t.Context(), "demo", "absent")
	if result.Package.Code != "component_not_found" {
		t.Fatalf("missing component: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, "mcp.json"), []byte(`{"mcpServers":{"main":{"type":"stdio","command":"must-never-run"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, _ = checker.CheckConnector(t.Context(), "demo", "main")
	if result.Package.Code != "unsupported_component" || result.Initialize.Status != "not_run" {
		t.Fatalf("stdio execution attempted: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, "mcp.json"), []byte(`{"mcpServers":{"main":{"type":"http","url":"https://example.test/mcp","disabled":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, _ = checker.CheckConnector(t.Context(), "demo", "main")
	if result.Package.Status != "passed" || result.Initialize.Code != "component_disabled" {
		t.Fatalf("disabled component misreported: %+v", result)
	}
}

func TestConnectorCheckBoundsMetadataAndAllowsZeroTools(t *testing.T) {
	for _, count := range []int{0, 101} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			remote := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "check-server", Version: "1.0.0"}, &sdkmcp.ServerOptions{Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}}})
			for i := 0; i < count; i++ {
				remote.AddTool(&sdkmcp.Tool{Name: fmt.Sprintf("read_%03d", i), Description: "private-metadata", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
					t.Error("checker called a business tool")
					return nil, errors.New("unexpected business call")
				})
			}
			server := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return remote }, &sdkmcp.StreamableHTTPOptions{JSONResponse: true}))
			defer server.Close()
			sources, _ := connectorCheckFixture(t, server.URL, "no_auth")
			result, err := NewConnectorChecker(sources).CheckConnector(t.Context(), "demo", "main")
			if err != nil || result.Status != "passed" || result.ToolDiscovery.Count != count || len(result.ToolDiscovery.Tools) != min(count, 100) || result.ToolDiscovery.Truncated != (count > 100) {
				t.Fatalf("metadata: %+v, %v", result, err)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "private-metadata") {
				t.Fatal("checker returned remote descriptions")
			}
		})
	}
}

func TestConnectorCheckRejectsUnsupportedNegotiatedProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["method"] != "initialize" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"protocolVersion": "2099-01-01", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1.0.0"}}})
	}))
	defer server.Close()
	sources, _ := connectorCheckFixture(t, server.URL, "no_auth")
	result, err := NewConnectorChecker(sources).CheckConnector(t.Context(), "demo", "main")
	if err != nil || result.Status != "failed" || result.Initialize.Code != "unsupported_protocol" || result.ToolDiscovery.Status != "not_run" {
		t.Fatalf("unsupported protocol passed: %+v, %v", result, err)
	}
}

func TestConnectorCheckSeparatesDiscoveryFailureFromInitialization(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "read_document", nil)
	defer upstream.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			data, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)
			if body["method"] == "tools/list" {
				http.Error(w, "private-discovery-error", http.StatusInternalServerError)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(data)))
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	sources, _ := connectorCheckFixture(t, server.URL, "no_auth")
	result, err := NewConnectorChecker(sources).CheckConnector(t.Context(), "demo", "main")
	data, _ := json.Marshal(result)
	if err != nil || result.Status != "failed" || result.Initialize.Status != "passed" || result.ToolDiscovery.Status != "failed" || strings.Contains(string(data), "private-") {
		t.Fatalf("discovery failure: %s, %v", data, err)
	}
}
