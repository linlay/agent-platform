package connectorops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDisconnectLetsDispatchedWebAppCallFinish(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	upstream := sdk.NewServer(&sdk.Implementation{Name: "inflight", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &sdk.CallToolResult{StructuredContent: map[string]any{"completed": true}}, nil
	})
	endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer endpoint.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	for name, value := range map[string]any{"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": nil}, "mcp.json": map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": endpoint.URL}}}} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := connectorauth.New(t.Context(), sources, nil)
	if _, err := m.Connect("demo"); err != nil {
		t.Fatal(err)
	}
	s := Service{Auth: m, Sources: sources}
	scope := Scope{Subject: "owner", AppID: "app", Execution: []Permission{{ConnectorID: "demo", Adapter: "mcp"}}, Check: func() error { return nil }}
	req := Request{ConnectorID: "demo", Adapter: "mcp", Component: "main", ToolName: "read", Arguments: map[string]any{}}
	done := make(chan error, 1)
	go func() { _, err := s.Invoke(t.Context(), scope, req); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("call did not start")
	}
	if _, err := m.Disconnect(t.Context(), "demo"); err != nil {
		close(release)
		t.Fatal(err)
	}
	select {
	case err := <-done:
		close(release)
		t.Fatal("disconnect interrupted business", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("completed result discarded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("business did not finish")
	}
	if _, err := s.Invoke(t.Context(), scope, req); err == nil || err.Error() != "connector_configuration_required" {
		t.Fatal("next call admitted", err)
	}
}
func TestConfiguredTokenCLIReachesExecution(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "connector.json"), []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"token","token_schema":{"fields":[{"key":"KEY","required":true}]}}`), 0600)
	os.WriteFile(filepath.Join(dir, "cli.json"), []byte(`{}`), 0600)
	m := connectorauth.New(t.Context(), sources, nil)
	auth, err := m.SetToken(t.Context(), "demo", map[string]string{"KEY": "fixture"})
	if err != nil || auth.Status != "configured" {
		t.Fatal(auth, err)
	}
	s := Service{Auth: m, Sources: sources}
	scope := Scope{Subject: "owner", AppID: "app", Execution: []Permission{{ConnectorID: "demo", Adapter: "cli"}}, Check: func() error { return nil }}
	_, err = s.Invoke(t.Context(), scope, Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"read"}})
	// This intentionally has no executable: it must pass auth and fail CLI setup.
	if err == nil || err.Error() == "connector_auth_required" || err.Error() == "connector_configuration_required" {
		t.Fatal("configured status rejected by auth", err)
	}
}
