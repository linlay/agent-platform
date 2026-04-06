package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestToolSyncLoadsStaticAndDiscoveredTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"tools":[{"key":"remote_tool","name":"remote_tool","description":"remote","parameters":{"type":"object"}}]}}`))
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\n"+
			"baseUrl: "+server.URL+"\n"+
			"tools:\n"+
			"  - key: static_tool\n"+
			"    name: static_tool\n"+
			"    description: static\n"+
			"    parameters:\n"+
			"      type: object\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	client := NewClient(registry, server.Client())
	tools, err := NewToolSync(registry, client).Load(context.Background())
	if err != nil {
		t.Fatalf("load tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "static_tool" {
		t.Fatalf("expected static mcp tool, got %#v", tools)
	}

	// Remove static tools and verify discovery path.
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\n"+
			"baseUrl: "+server.URL+"\n",
	), 0o644); err != nil {
		t.Fatalf("rewrite registry file: %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	tools, err = NewToolSync(registry, client).Load(context.Background())
	if err != nil {
		t.Fatalf("load discovered tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_tool" || tools[0].Meta["kind"] != "mcp" {
		t.Fatalf("expected discovered mcp tool, got %#v", tools)
	}
}

func TestClientCallToolUsesJSONRPC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"status":"ok"}}`))
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\n"+
			"baseUrl: "+server.URL+"\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	client := NewClient(registry, server.Client())
	result, err := client.CallTool(context.Background(), "demo", "tool_a", map[string]any{"value": 1})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result["status"] != "ok" {
		t.Fatalf("expected ok result, got %#v", result)
	}
}
