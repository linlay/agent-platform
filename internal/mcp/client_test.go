package mcp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/retry"
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

func TestRegistryLoadsServerTimeoutSeconds(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\n"+
			"baseUrl: http://127.0.0.1:11969\n"+
			"connect-timeout: 3\n"+
			"read-timeout: 15\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	server, ok := registry.Server("demo")
	if !ok {
		t.Fatal("expected demo server to load")
	}
	if server.ConnectTimeout != 3 || server.ReadTimeout != 15 {
		t.Fatalf("expected second-based timeouts, got %#v", server)
	}
}

func TestRegistryRejectsDeprecatedServerTimeoutMs(t *testing.T) {
	cases := []struct {
		name       string
		field      string
		wantErrSub string
	}{
		{name: "camel connect", field: "connectTimeoutMs: 3000\n", wantErrSub: "connectTimeoutMs"},
		{name: "kebab connect", field: "connect-timeout-ms: 3000\n", wantErrSub: "connect-timeout-ms"},
		{name: "camel read", field: "readTimeoutMs: 15000\n", wantErrSub: "readTimeoutMs"},
		{name: "kebab read", field: "read-timeout-ms: 15000\n", wantErrSub: "read-timeout-ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
				"key: demo\n"+
					"baseUrl: http://127.0.0.1:11969\n"+
					tc.field,
			), 0o644); err != nil {
				t.Fatalf("write registry file: %v", err)
			}

			_, err := NewRegistry(root)
			if err == nil {
				t.Fatal("expected deprecated timeout field to be rejected")
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) || !strings.Contains(err.Error(), "timeout") {
				t.Fatalf("expected migration error for %s, got %v", tc.wantErrSub, err)
			}
		})
	}
}

func TestRegistryRejectsDeprecatedStaticToolMetaTimeoutMs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\n"+
			"baseUrl: http://127.0.0.1:11969\n"+
			"tools:\n"+
			"  - name: static_tool\n"+
			"    meta:\n"+
			"      timeoutMs: 600000\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}

	_, err := NewRegistry(root)
	if err == nil {
		t.Fatal("expected deprecated tool meta timeoutMs to be rejected")
	}
	if !strings.Contains(err.Error(), "meta.timeoutMs") || !strings.Contains(err.Error(), "meta.timeout") {
		t.Fatalf("expected migration error for meta.timeoutMs, got %v", err)
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

func TestAvailabilityGateReadyToRetryNormalizesKeys(t *testing.T) {
	gate := NewAvailabilityGate()
	gate.MarkFailure(" Demo ")
	gate.mu.Lock()
	gate.nextRetry["demo"] = time.Now().Add(-time.Second)
	gate.mu.Unlock()

	ready := gate.ReadyToRetry([]string{" demo "})
	if len(ready) != 1 || ready[0] != "demo" {
		t.Fatalf("expected normalized ready key, got %#v", ready)
	}
}

func TestAvailabilityGateBackoffPolicyAndReset(t *testing.T) {
	gate := NewAvailabilityGateWithPolicy(retryPolicyForTest(10*time.Millisecond, 80*time.Millisecond))
	gate.MarkFailure(" Demo ")
	if got := gate.currentBackoff["demo"]; got != 10*time.Millisecond {
		t.Fatalf("first backoff = %s, want 10ms", got)
	}
	gate.MarkFailure("demo")
	if got := gate.currentBackoff["demo"]; got != 20*time.Millisecond {
		t.Fatalf("second backoff = %s, want 20ms", got)
	}
	gate.MarkSuccess(" demo ")
	if gate.IsUnavailable("demo") {
		t.Fatal("expected success to clear unavailable state")
	}
	if got := gate.currentBackoff["demo"]; got != 0 {
		t.Fatalf("expected success to reset backoff, got %s", got)
	}
}

func retryPolicyForTest(min time.Duration, max time.Duration) retry.BackoffPolicy {
	return retry.BackoffPolicy{Min: min, Max: max, Factor: 2}
}
