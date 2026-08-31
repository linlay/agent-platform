package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/retry"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolSyncLoadsStaticAndDiscoveredTools(t *testing.T) {
	server := newSDKMCPTestServer(t, "remote_tool", nil)
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
	defer client.Close()
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
	if len(tools) != 1 || tools[0].Name != "remote_tool" || tools[0].Meta["sourceType"] != "mcp" || tools[0].Meta["sourceCategory"] != "mcp" || tools[0].Meta["sourceKey"] != "demo" {
		t.Fatalf("expected discovered mcp tool, got %#v", tools)
	}
}

func TestClientCallToolUsesJSONRPC(t *testing.T) {
	server := newSDKMCPTestServer(t, "tool_a", map[string]any{"status": "ok"})
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
	defer client.Close()
	result, err := client.CallTool(context.Background(), "demo", "tool_a", map[string]any{"value": 1}, map[string]any{"toolName": "tool_a"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	resultMap, _ := result.(map[string]any)
	structured, _ := resultMap["structuredContent"].(map[string]any)
	if structured["status"] != "ok" {
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

func TestRegistryReloadKeepsVersionForSemanticNoop(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.yml")
	if err := os.WriteFile(path, []byte("key: demo\nbaseUrl: http://127.0.0.1:11969\n"), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	version := registry.Version()
	if err := os.WriteFile(path, []byte("# comment only\nkey: demo\nbaseUrl: http://127.0.0.1:11969\n"), 0o644); err != nil {
		t.Fatalf("rewrite registry file: %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if registry.Version() != version {
		t.Fatalf("semantic no-op changed registry version from %d to %d", version, registry.Version())
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

func TestToolSyncSkipsUnavailableServersAndKeepsReachableTools(t *testing.T) {
	reachable := newSDKMCPTestServer(t, "remote_tool", nil)
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
	defer client.Close()
	syncer := NewToolSync(registry, client)
	tools, err := syncer.Load(context.Background())
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
	if status, ok := syncer.ServerStatus("reachable"); !ok || status.Status != ToolSyncStatusReady || status.LastSyncSuccessAt == 0 || status.Diagnostic != nil {
		t.Fatalf("reachable sync status = %#v, found=%v", status, ok)
	}
	if status, ok := syncer.ServerStatus("dead"); !ok || status.Status != ToolSyncStatusUnavailable || status.LastSyncAttemptAt == 0 || status.Diagnostic == nil || status.Diagnostic.Code != "mcp_sync_failed" {
		t.Fatalf("dead sync status = %#v, found=%v", status, ok)
	}
}

func TestToolSyncRetainsLastKnownToolsAndRecovers(t *testing.T) {
	server, unavailable := newToggleableSDKMCPTestServer(t, "remote_tool")
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\n"+
			"baseUrl: "+server.URL+"\n"+
			"retry: 0\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	gate := NewAvailabilityGate()
	client := NewClientWithGate(registry, server.Client(), gate)
	defer client.Close()
	syncer := NewToolSync(registry, client)

	if tools, err := syncer.Load(context.Background()); err != nil || len(tools) != 1 {
		t.Fatalf("initial load tools=%#v err=%v", tools, err)
	}
	unavailable.Store(true)
	result, err := syncer.RefreshServersWithResult(context.Background(), []string{"demo"})
	if err != nil {
		t.Fatalf("refresh unavailable server: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != "remote_tool" {
		t.Fatalf("last-known tools were not retained: %#v", result.Tools)
	}
	if status, ok := syncer.ServerStatus("demo"); !ok || status.Status != ToolSyncStatusUnavailable || status.Diagnostic == nil {
		t.Fatalf("unavailable status = %#v, found=%v", status, ok)
	}

	unavailable.Store(false)
	gate.MarkSuccess("demo")
	result, err = syncer.RefreshServersWithResult(context.Background(), []string{"demo"})
	if err != nil {
		t.Fatalf("refresh recovered server: %v", err)
	}
	if !result.Changed {
		t.Fatal("recovery should report a changed sync state")
	}
	if status, ok := syncer.ServerStatus("demo"); !ok || status.Status != ToolSyncStatusReady || status.Diagnostic != nil {
		t.Fatalf("recovered status = %#v, found=%v", status, ok)
	}

	if err := os.Remove(filepath.Join(root, "server.yml")); err != nil {
		t.Fatalf("remove registry file: %v", err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatalf("reload removed registry: %v", err)
	}
	if tools, err := syncer.Load(context.Background()); err != nil || len(tools) != 0 {
		t.Fatalf("removed server tools=%#v err=%v", tools, err)
	}
	if status, ok := syncer.ServerStatus("demo"); ok {
		t.Fatalf("removed server retained sync status: %#v", status)
	}
}

func TestReconnectLoopBroadcastsRecoveredToolState(t *testing.T) {
	server, unavailable := newToggleableSDKMCPTestServer(t, "remote_tool")
	defer server.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.yml"), []byte(
		"key: demo\nbaseUrl: "+server.URL+"\nretry: 0\n",
	), 0o644); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	gate := NewAvailabilityGateWithPolicy(retryPolicyForTest(time.Millisecond, time.Millisecond))
	client := NewClientWithGate(registry, server.Client(), gate)
	defer client.Close()
	syncer := NewToolSync(registry, client)
	unavailable.Store(true)
	if _, err := syncer.Load(context.Background()); err != nil {
		t.Fatalf("load unavailable server: %v", err)
	}
	unavailable.Store(false)
	notifications := &recordingMCPNotificationSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewReconnectLoop(registry, syncer, gate, time.Millisecond, notifications).Start(ctx)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if notifications.count("catalog.updated") > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("reconnect recovery did not broadcast catalog.updated")
}

type recordingMCPNotificationSink struct {
	mu     sync.Mutex
	events []string
}

func (s *recordingMCPNotificationSink) Broadcast(eventType string, _ map[string]any) {
	s.mu.Lock()
	s.events = append(s.events, eventType)
	s.mu.Unlock()
}

func (s *recordingMCPNotificationSink) count(eventType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, event := range s.events {
		if event == eventType {
			count++
		}
	}
	return count
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

func TestSanitizeSyncErrorRedactsConfiguredSecrets(t *testing.T) {
	message := sanitizeSyncError(ServerDefinition{
		AuthToken: "token-secret",
		Headers:   map[string]string{"X-API-Key": "header-secret"},
		Env:       map[string]string{"PRIVATE_KEY": "env-secret"},
	}, errors.New("initialize token-secret header-secret env-secret failed"))
	for _, secret := range []string{"token-secret", "header-secret", "env-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("sync diagnostic leaked %q: %s", secret, message)
		}
	}
}

func TestHeaderRoundTripperReadsIdentityFileOnEveryRequest(t *testing.T) {
	identityFile := filepath.Join(t.TempDir(), "sso-access-token.txt")
	tokenA := unsignedJWTWithIssuer(t, "https://eiam.example.test/auth/oidc/dev", "subject-a")
	tokenB := unsignedJWTWithIssuer(t, "https://eiam.example.test/auth/oidc/dev", "subject-b")
	if err := os.WriteFile(identityFile, []byte(tokenA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var authorizations []string
	transport := headerRoundTripper{
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			authorizations = append(authorizations, request.Header.Get("Authorization"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}),
		authSource:     AuthSourceIdentityFile,
		identityFile:   identityFile,
		configuredHost: "example.test",
	}
	request, err := http.NewRequest(http.MethodPost, "https://example.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := os.WriteFile(identityFile, []byte(tokenB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if got := strings.Join(authorizations, ","); got != "Bearer "+tokenA+",Bearer "+tokenB {
		t.Fatalf("authorization headers = %q", got)
	}
}

func TestHeaderRoundTripperRejectsIdentityFileOutsideConfiguredMCPHost(t *testing.T) {
	identityFile := filepath.Join(t.TempDir(), "sso-access-token.txt")
	if err := os.WriteFile(identityFile, []byte("identity-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := headerRoundTripper{
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("untrusted request reached base transport: %s", request.URL)
			return nil, nil
		}),
		authSource: AuthSourceIdentityFile, identityFile: identityFile, configuredHost: "api.resource.test",
	}
	request, err := http.NewRequest(http.MethodPost, "https://attacker.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "left the configured MCP host") {
		t.Fatalf("expected configured MCP host rejection, got %v", err)
	}
}

func unsignedJWTWithIssuer(t *testing.T, issuer string, subject string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"iss": issuer, "sub": subject})
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
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

func newSDKMCPTestServer(t *testing.T, toolName string, callResult map[string]any) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server", Version: "1.0.0"}, &sdkmcp.ServerOptions{
		Capabilities: &sdkmcp.ServerCapabilities{},
	})
	server.AddTool(&sdkmcp.Tool{
		Name:        toolName,
		Description: "remote",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Meta: sdkmcp.Meta{
			"sourceType":     "local",
			"sourceCategory": "external",
			"sourceKey":      "wrong",
		},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		result := callResult
		if result == nil {
			result = map[string]any{"ok": true}
		}
		data, _ := json.Marshal(result)
		return &sdkmcp.CallToolResult{
			Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
			StructuredContent: result,
		}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{JSONResponse: true})
	return httptest.NewServer(handler)
}

func newToggleableSDKMCPTestServer(t *testing.T, toolName string) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server", Version: "1.0.0"}, &sdkmcp.ServerOptions{
		Capabilities: &sdkmcp.ServerCapabilities{},
	})
	server.AddTool(&sdkmcp.Tool{
		Name:        toolName,
		Description: "remote",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{JSONResponse: true})
	unavailable := &atomic.Bool{}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	return httpServer, unavailable
}
