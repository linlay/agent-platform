package server

// Opt-in local integration fixture: production Platform HTTP handlers and real MCP
// validation, with only the upstream service mocked. All data lives in t.TempDir.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	platformmcp "agent-platform/internal/mcp"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDesktopConnectorBridgeLocal(t *testing.T) {
	output := os.Getenv("DESKTOP_CONNECTOR_BRIDGE_FILE")
	if output == "" {
		t.Skip("opt-in Desktop integration fixture")
	}
	sdk := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "desktop-bridge-upstream", Version: "1"}, nil)
	sdk.AddTool(&sdkmcp.Tool{Name: "read_document", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "fixture document"}}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return sdk }, &sdkmcp.StreamableHTTPOptions{JSONResponse: true})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token-mcp" && r.Header.Get("X-API-Key") != "fixture-valid-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	fixture := newTestFixture(t)
	root := fixture.server.deps.Config.Paths.EffectiveConnectorsCenterDir()
	for _, id := range []string{"bridge-none", "bridge-token", "bridge-oauth"} {
		writeMCPConnectorForTest(t, root, id)
		manifest := map[string]any{"id": id, "name": id, "version": "1.0.0", "type": "mcp", "authMode": "self"}
		component := map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"}
		files := map[string]any{"connector.json": manifest, "mcp.json": map[string]any{"mcpServers": map[string]any{"main": component}}}
		if id == "bridge-token" {
			manifest["authMode"] = "token"
			files["token-schema.json"] = map[string]any{"fields": []map[string]any{{"key": "API_KEY", "label": "API key", "type": "password", "required": true}}}
			component["url"] = upstream.URL + "/token-mcp"
			component["headers"] = map[string]any{"X-API-Key": "${API_KEY}"}
		}
		if id == "bridge-oauth" {
			manifest["authMode"] = "oauth"
			manifest["authBrowser"] = "embedded"
			manifest["oauth"] = map[string]any{"clientId": "fixture-client", "authorizationEndpoint": upstream.URL + "/authorize", "tokenEndpoint": upstream.URL + "/token", "resource": upstream.URL + "/mcp", "scopes": []string{"read"}}
		}
		for name, value := range files {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, id, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := platformmcp.NewRegistryWithSources(fixture.server.connectorSources())
	if err != nil {
		t.Fatal(err)
	}
	client := platformmcp.NewClientWithGate(registry, upstream.Client(), platformmcp.NewAvailabilityGate())
	defer client.Close()
	fixture.server.deps.MCP = client
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"url": server.URL, "upstream": upstream.URL, "runtimeRoot": filepath.Dir(root), "connectorsRoot": root, "ids": []string{"bridge-none", "bridge-token", "bridge-oauth"}, "token": "fixture-valid-token"})
	if err = os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Desktop bridge ready at %s; stop by creating %s.stop", server.URL, output)
	for deadline := time.Now().Add(20 * time.Minute); time.Now().Before(deadline); {
		if _, err := os.Stat(output + ".stop"); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("Desktop bridge timed out")
}
