package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeOneIDFixture(t *testing.T, root string, component map[string]any) connector.Package {
	t.Helper()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "oneid-token"},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": component}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := connector.Load(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestOneIDHTTPUsesCurrentEnvironmentAndBoundDestination(t *testing.T) {
	root := t.TempDir()
	pkg := writeOneIDFixture(t, root, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp/", "headers": map[string]any{"Authorization": "Bearer ${AP_ACCESS_TOKEN}", "X-SSO": "${AP_ACCESS_TOKEN}"}})
	definition, err := connectorServer(pkg, "main")
	if err != nil || definition.SetupError != "" || definition.AuthSource != AuthSourceIdentityFile || !definition.ConnectorOneID {
		t.Fatal("oneid registry", err)
	}
	file := filepath.Join(t.TempDir(), "sso-access-token.txt")
	expected := "private-token-a"
	calls := 0
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer "+expected || req.Header.Get("X-SSO") != expected {
			t.Error("wrong SSO environment")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	client := (&Client{httpClient: &http.Client{Transport: base}}).WithIdentityFile(file).httpClientForServer(definition)
	for _, value := range []string{"private-token-a", "private-token-b"} {
		expected = value
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, definition.ResolvedURL(), nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Fatal("SSO escaped into caller request")
		}
	}
	for _, bad := range []string{"https://example.test/other", "https://other.test/mcp/", "https://example.test/mcp/?x=1", "http://example.test/mcp/"} {
		if _, err := client.Get(bad); err == nil {
			t.Fatal("SSO escaped destination")
		}
	}
	os.Remove(file)
	if _, err := client.Get(definition.ResolvedURL()); err == nil || calls != 2 {
		t.Fatal("Desktop logout left HTTP authorization active")
	}
	data, _ := json.Marshal(definition)
	if strings.Contains(string(data), "private-token") {
		t.Fatal("SSO token entered definition")
	}
	if _, err := os.Stat(filepath.Join(pkg.PersistentRoot(), pkg.ID)); !os.IsNotExist(err) {
		t.Fatal("SSO copied to connector state")
	}
}

func TestOneIDStdioInjectionRotationAndLogout(t *testing.T) {
	t.Setenv(agentconfig.EnvAccessToken, "inherited-spoof")
	root := t.TempDir()
	pkg := writeOneIDFixture(t, root, map[string]any{
		"type": "stdio", "command": os.Args[0], "args": []string{"-test.run=^TestOneIDStdioHelperProcess$"},
		"env": map[string]any{"AP_ONEID_TEST_HELPER": "1", "SERVICE_TOKEN": "${AP_ACCESS_TOKEN}"},
	})
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sso-access-token.txt")
	client := NewClientWithGate(registry, nil, nil).WithIdentityFile(file)
	defer client.Close()
	definition, _ := registry.Server("demo")
	if _, err := client.CallTool(t.Context(), "demo", "check_identity", nil, nil); err == nil {
		t.Fatal("stdio started without SSO")
	}
	var previousPID int
	for _, generation := range []string{"a", "b"} {
		os.WriteFile(file, []byte("identity-"+generation), 0600)
		result, err := client.CallTool(t.Context(), "demo", "check_identity", map[string]any{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := mcpResultPID(result)
		if err != nil {
			t.Fatal(err)
		}
		mapped, _ := result.(map[string]any)
		structured, _ := mapped["structuredContent"].(map[string]any)
		if structured["generation"] != generation || structured["matches"] != true {
			t.Fatal("incorrect child environment")
		}
		if previousPID != 0 {
			if pid == previousPID {
				t.Fatal("SSO rotation reused old process")
			}
			waitForProcessExit(t, previousPID)
		}
		previousPID = pid
		client.Reconcile()
		repeated, err := client.CallTool(t.Context(), "demo", "check_identity", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		samePID, err := mcpResultPID(repeated)
		if err != nil || samePID != pid {
			t.Fatal("unchanged SSO session was rebuilt by reconcile", err)
		}
	}
	os.Remove(file)
	if _, err := client.CallTool(t.Context(), "demo", "check_identity", nil, nil); err == nil {
		t.Fatal("old stdio session survived Desktop logout")
	}
	waitForProcessExit(t, previousPID)
	definition.ConnectorOneID = false
	definition.Env = map[string]string{"SAFE": "value"}
	identity, err := client.stdioIdentity(definition)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := client.transportWithIdentity(definition, identity)
	if err != nil {
		t.Fatal(err)
	}
	cmd := transport.(*sdkmcp.CommandTransport).Command
	for _, item := range cmd.Env {
		if strings.HasPrefix(strings.ToUpper(item), "AP_ACCESS_TOKEN=") {
			t.Fatal("ordinary MCP inherited SSO")
		}
	}
	if os.Getenv(agentconfig.EnvAccessToken) != "inherited-spoof" {
		t.Fatal("changed Platform process environment")
	}
	if _, err := os.Stat(filepath.Join(pkg.PersistentRoot(), pkg.ID)); !os.IsNotExist(err) {
		t.Fatal("stdio SSO persisted to connector state")
	}
}

func TestOneIDStdioHelperProcess(t *testing.T) {
	if os.Getenv("AP_ONEID_TEST_HELPER") != "1" {
		return
	}
	token := os.Getenv(agentconfig.EnvAccessToken)
	generation := map[string]string{"identity-a": "a", "identity-b": "b"}[token]
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "oneid-test", Version: "1.0.0"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "check_identity", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		value := map[string]any{"pid": os.Getpid(), "generation": generation, "matches": token != "" && token == os.Getenv("SERVICE_TOKEN")}
		return &sdkmcp.CallToolResult{StructuredContent: value}, nil
	})
	if err := server.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestOneIDLocalValidationAndLegacyClassification(t *testing.T) {
	for _, component := range []map[string]any{
		{"type": "streamableHttp", "url": "http://example.test/mcp"},
		{"type": "streamableHttp", "url": "https://example.test:8443/mcp"},
		{"type": "streamableHttp", "url": "https://example.test/mcp", "headers": map[string]any{"Authorization": "Bearer fixed"}},
		{"type": "streamableHttp", "url": "https://example.test/mcp", "headers": map[string]any{"X-Key": "${OTHER_TOKEN}"}},
		{"type": "stdio", "command": os.Args[0], "env": map[string]any{"AP_ACCESS_TOKEN": "fixed"}},
	} {
		pkg := writeOneIDFixture(t, t.TempDir(), component)
		if _, err := connectorServer(pkg, "main"); err == nil {
			t.Fatal("invalid oneid configuration accepted")
		}
	}
	root := t.TempDir()
	pkg := writeOneIDFixture(t, root, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp", "platform": map[string]any{"authSource": "identity-file"}})
	path := filepath.Join(pkg.Dir, "connector.json")
	raw := []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"none"}`)
	os.WriteFile(path, raw, 0644)
	loaded, err := connector.Load(root, "demo")
	if err != nil || loaded.AuthMode != connector.AuthOneID {
		t.Fatal("legacy identity not classified", err)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(raw) {
		t.Fatal("classification rewrote source")
	}
}
