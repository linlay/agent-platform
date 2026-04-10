package mcp

import (
	"context"
	"net"
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
	if len(tools) != 1 || tools[0].Name != "remote_tool" {
		t.Fatalf("expected discovered mcp tool, got %#v", tools)
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
	if len(tools) != 1 || tools[0].Name != "remote_tool" || tools[0].Meta["sourceType"] != "mcp" {
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
	result, err := client.CallTool(context.Background(), "demo", "tool_a", map[string]any{"value": 1}, map[string]any{"toolName": "tool_a"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	resultMap, _ := result.(map[string]any)
	if resultMap["status"] != "ok" {
		t.Fatalf("expected ok result, got %#v", result)
	}
}

func TestRegistrySkipsExampleServerFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.yml"), []byte(
		"key: demo\n"+
			"baseUrl: http://127.0.0.1:11969\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.example.yml"), []byte(
		"key: ignored\n"+
			"baseUrl: http://127.0.0.1:11970\n",
	), 0o644); err != nil {
		t.Fatalf("write example registry file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if _, ok := registry.Server("demo"); !ok {
		t.Fatalf("expected demo server to load")
	}
	if _, ok := registry.Server("ignored"); ok {
		t.Fatalf("did not expect ignored example server to load")
	}
}

func TestToolSyncSkipsUnavailableServersAndKeepsReachableTools(t *testing.T) {
	reachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"tools":[{"key":"remote_tool","name":"remote_tool","description":"remote","parameters":{"type":"object"}}]}}`))
	}))
	defer reachable.Close()

	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for dead endpoint: %v", err)
	}
	deadURL := "http://" + deadListener.Addr().String()
	_ = deadListener.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "reachable.yml"), []byte(
		"key: reachable\n"+
			"baseUrl: "+reachable.URL+"\n",
	), 0o644); err != nil {
		t.Fatalf("write reachable registry file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dead.yml"), []byte(
		"key: dead\n"+
			"baseUrl: "+deadURL+"\n",
	), 0o644); err != nil {
		t.Fatalf("write dead registry file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	gate := NewAvailabilityGate()
	client := NewClientWithGate(registry, reachable.Client(), gate)
	tools, err := NewToolSync(registry, client).Load(context.Background())
	if err != nil {
		t.Fatalf("load tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "remote_tool" {
		t.Fatalf("expected reachable tools only, got %#v", tools)
	}
	if !gate.IsUnavailable("dead") {
		t.Fatalf("expected dead server to be marked unavailable")
	}
	if gate.IsUnavailable("reachable") {
		t.Fatalf("expected reachable server to remain available")
	}
}
