package connectorops

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"context"
	"encoding/json"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestInvokeMCPReusesExistingConnectorCredentials(t *testing.T) {
	var calls atomic.Int32
	upstream := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{StructuredContent: map[string]any{"items": []any{}}}, nil
	})
	upstream.AddTool(&sdk.Tool{Name: "text-error", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "business rejected"}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Key") != "alice-key" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	files := map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "KEY", "type": "password", "required": true}}}},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": server.URL, "headers": map[string]any{"X-Key": "${KEY}"}}}},
	}
	for name, value := range files {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := connectorauth.New(context.Background(), sources, nil)
	if _, err := manager.SetToken(context.Background(), "demo", map[string]string{"KEY": "alice-key"}); err != nil {
		t.Fatal(err)
	}
	pkg, _ := sources.Load("demo")
	native, err := callMCP(context.Background(), pkg, "main", "text-error", map[string]any{})
	var original map[string]any
	if err != nil || json.Unmarshal(native, &original) != nil || original["isError"] != true {
		t.Fatal("native MCP result lost", string(native), err)
	}
	service := Service{Auth: manager, Sources: sources}
	scope := Scope{Subject: "alice", AppID: "calendar", Execution: []Permission{{"demo", "mcp"}}, Check: func() error { return nil }}
	_, err = service.Describe(scope, "demo")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{ConnectorID: "demo", Adapter: "mcp", Component: "main", ToolName: "read", Arguments: map[string]any{}}
	result, err := service.Invoke(context.Background(), scope, req)
	if err != nil || len(result.MCP) == 0 || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	release, err := connector.AcquireOperation(sources.ExternalRoot, "demo")
	if err != nil {
		t.Fatal(err)
	}
	_, busyErr := service.Invoke(context.Background(), scope, req)
	release()
	if busyErr == nil || busyErr.Error() != "connector_busy" || calls.Load() != 1 {
		t.Fatal("package mutation lock bypassed", busyErr)
	}
	scope.Subject = "bob"
	if _, err = service.Invoke(context.Background(), scope, req); err != nil || calls.Load() != 2 {
		t.Fatal("application subject must not select connector credentials", err)
	}
	scope.Subject = "alice"
	req.ToolName = "undeclared"
	if _, err = service.Invoke(context.Background(), scope, req); err == nil || calls.Load() != 2 {
		t.Fatal("invalid input executed", err)
	}
	if err := manager.Logout(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	req.ToolName = "read"
	if _, err = service.Invoke(context.Background(), scope, req); err == nil || err.Error() != "connector_auth_required" || calls.Load() != 2 {
		t.Fatal("logout not shared", err)
	}
	if _, err := os.Stat(filepath.Join(sources.StateRoot, "users")); !os.IsNotExist(err) {
		t.Fatal("unexpected multi-user state", err)
	}
}

func TestCLIHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "connectorops-helper" {
		return
	}
	if os.Getenv("UNRELATED_SECRET") != "" {
		os.Exit(4)
	}
	var value map[string]any
	if json.Unmarshal([]byte(os.Args[len(os.Args)-1]), &value) != nil {
		os.Exit(5)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"value": value["value"], "config": os.Getenv("DEMO_CONFIG_DIR")})
	os.Exit(0)
}
func TestCLIUsesSingleJSONArgumentWithoutShellOrAmbientCredentials(t *testing.T) {
	t.Setenv("UNRELATED_SECRET", "do-not-inherit")
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0700)
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	entry := "bin/helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	if err = os.WriteFile(filepath.Join(dir, entry), data, 0700); err != nil {
		t.Fatal(err)
	}
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo"}, Dir: dir, StateRoot: t.TempDir(), BinDir: bin, CLI: map[string]any{"platform": map[string]any{"configEnv": "DEMO_CONFIG_DIR", "command": "helper"}, "versionCheck": map[string]any{"command": map[string]any{"darwin": "helper --version", "linux": "helper --version", "win32": "helper.cmd --version"}, "minVersion": "1.0.0"}}}
	malicious := `$(touch should-not-exist); " %PATH% & | < >`
	args, _ := json.Marshal(map[string]any{"value": malicious})
	output, err := callCLI(context.Background(), pkg, []string{"-test.run=^TestCLIHelper$", "--", "connectorops-helper", "--json", string(args)})
	var decoded map[string]any
	json.Unmarshal([]byte(output.Stdout), &decoded)
	if err != nil || decoded["value"] != malicious {
		t.Fatal(output, err)
	}
	if decoded["config"] != filepath.Join(pkg.StateRoot, "demo", "config") {
		t.Fatal("wrong credential locator", output)
	}
	if _, err = os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("shell ran")
	}
}
