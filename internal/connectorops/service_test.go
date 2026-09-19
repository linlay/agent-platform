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

func TestInvokeMCPWithPersonalCredentialsAndExplicitOperation(t *testing.T) {
	var calls atomic.Int32
	upstream := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{StructuredContent: map[string]any{"items": []any{}}}, nil
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
		"connector.json":  map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "KEY", "type": "password", "required": true}}}},
		"mcp.json":        map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": server.URL, "headers": map[string]any{"X-Key": "${KEY}"}}}},
		"operations.json": map[string]any{"version": 1, "operations": []any{map[string]any{"operationId": "read", "description": "Read", "effect": "read", "adapter": "mcp", "mcp": map[string]any{"component": "main", "tool": "read"}, "inputSchema": map[string]any{"type": "object", "additionalProperties": false}, "outputSchema": map[string]any{"type": "object", "required": []string{"items"}, "properties": map[string]any{"items": map[string]any{"type": "array"}}, "additionalProperties": false}}}},
	}
	for name, value := range files {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := connectorauth.New(context.Background(), sources, nil)
	alice, _ := manager.Personal("alice", "default")
	if _, err := alice.SetToken(context.Background(), "demo", map[string]string{"KEY": "alice-key"}); err != nil {
		t.Fatal(err)
	}
	service := Service{Auth: manager, Sources: sources}
	scope := Scope{Subject: "alice", AppID: "calendar", Operations: map[string][]string{"demo": {"read"}}, Check: func() error { return nil }}
	catalog, err := service.Describe(scope, "demo")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{ConnectorID: "demo", OperationID: "read", Revision: catalog.Revision, Arguments: map[string]any{}}
	result, err := service.Invoke(context.Background(), scope, req)
	if err != nil || result.Status != "succeeded" || calls.Load() != 1 {
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
	if _, err = service.Invoke(context.Background(), scope, req); err == nil || calls.Load() != 1 {
		t.Fatal("cross-owner invocation", err)
	}
	scope.Subject = "alice"
	req.Arguments = map[string]any{"shell": "danger"}
	if _, err = service.Invoke(context.Background(), scope, req); err == nil || calls.Load() != 1 {
		t.Fatal("invalid input executed", err)
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
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo"}, Dir: dir, StateRoot: t.TempDir(), CLI: map[string]any{"platform": map[string]any{"configEnv": "DEMO_CONFIG_DIR"}}}
	malicious := `$(touch should-not-exist); " %PATH% & | < >`
	args, _ := json.Marshal(map[string]any{"value": malicious})
	output, err := callCLI(context.Background(), pkg, CLI{Entry: entry, Args: []string{"-test.run=^TestCLIHelper$", "--", "connectorops-helper"}, JSONFlag: "--json"}, args)
	if err != nil || output["value"] != malicious {
		t.Fatal(output, err)
	}
	if output["config"] != filepath.Join(pkg.StateRoot, "demo", "config") {
		t.Fatal("wrong credential locator", output)
	}
	if _, err = os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("shell ran")
	}
}
