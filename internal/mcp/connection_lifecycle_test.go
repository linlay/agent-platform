package mcp

import (
	"agent-platform/internal/connector"
	"context"
	"encoding/json"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestCachedMCPClientChecksConfigurationOnEveryCall(t *testing.T) {
	server := newSDKMCPTestServer(t, "read", nil)
	defer server.Close()
	root := t.TempDir()
	if err := writeConnectorFixture(filepath.Join(root, "server.yml"), []byte("key: demo\nbaseUrl: "+server.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClientWithGate(registry, server.Client(), nil)
	defer client.Close()
	if _, err := client.CallTool(t.Context(), "demo", "read", nil, nil); err != nil {
		t.Fatal(err)
	}
	pkg, err := (connector.Sources{ExternalRoot: root}).Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.SetConfigured(false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(t.Context(), "demo", "read", nil, nil); err == nil {
		t.Fatal("cached session bypassed cleared configuration")
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(t.Context(), "demo", "read", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialReconcileDrainsInFlightMCPCall(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	upstream := sdk.NewServer(&sdk.Implementation{Name: "drain", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &sdk.CallToolResult{StructuredContent: map[string]any{"done": true}}, nil
	})
	endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer endpoint.Close()
	root := t.TempDir()
	if err := writeConnectorFixture(filepath.Join(root, "server.yml"), []byte("key: demo\nbaseUrl: "+endpoint.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClientWithGate(registry, endpoint.Client(), nil)
	defer client.Close()
	done := make(chan error, 1)
	go func() { _, err := client.CallTool(t.Context(), "demo", "read", nil, nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("call did not start")
	}
	pkg, err := (connector.Sources{ExternalRoot: root}).Load("demo")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err = pkg.SetConfigured(false); err != nil {
		close(release)
		t.Fatal(err)
	}
	// Credential watcher rebuilds definitions with a different credential revision.
	registry.mu.Lock()
	for key, server := range registry.servers {
		server.CredentialRevision = "signed-out"
		registry.servers[key] = server
	}
	registry.mu.Unlock()
	client.Reconcile()
	select {
	case err := <-done:
		close(release)
		t.Fatal("reconcile interrupted accepted call", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call did not finish")
	}
	if _, err := client.CallTool(t.Context(), "demo", "read", nil, nil); err == nil {
		t.Fatal("new call bypassed cleared configuration")
	}
}
