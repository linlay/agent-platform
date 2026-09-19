package connectorops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCLIParameterTransportAndReadSQL(t *testing.T) {
	op := CLI{Args: []string{"records", "query"}, Parameters: []CLIParameter{{Field: "docid", Flag: "--docid"}, {Field: "sql", Flag: "--sql", Repeat: true, ReadSQL: true}}}
	sql := `SELECT COUNT(*) FROM ` + "`事项`" + ` WHERE ` + "`处理人`" + ` LIKE "%张倩%"`
	raw, _ := json.Marshal(map[string]any{"docid": "a & b", "sql": []string{sql, `SELECT "$(touch nope)"`}})
	args, err := cliArguments(op, raw)
	want := []string{"records", "query", "--docid", "a & b", "--sql", sql, "--sql", `SELECT "$(touch nope)"`}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatal(args, err)
	}
	for _, bad := range []string{`DELETE FROM x`, `SELECT 1; DELETE FROM x`, `SELECT 1 -- comment`, `SELECT 1 INTO OUTFILE "x"`, `SELECT LOAD_FILE("x")`, `SELECT /* hidden */ 1`} {
		raw, _ := json.Marshal(map[string]any{"sql": []string{bad}})
		if _, err := cliArguments(op, raw); err == nil {
			t.Fatal("unsafe query", bad)
		}
	}
	if cliEntryForOS(CLI{Entry: "bin/client", EntryWindows: "bin/client.exe"}, "windows") != "bin/client.exe" || cliEntryForOS(CLI{Entry: "bin/client", EntryWindows: "bin/client.exe"}, "darwin") != "bin/client" {
		t.Fatal("OS mapping")
	}
}

func TestShippedProfileDoesNotModifyPackageAndStripsIdentityContext(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bin/libs"), 0700)
	executable, _ := os.Executable()
	data, _ := os.ReadFile(executable)
	for _, name := range []string{"wecom-cli", "wecom-cli.exe"} {
		os.WriteFile(filepath.Join(dir, "bin/libs", name), data, 0700)
	}
	pkg := connector.Package{Manifest: connector.Manifest{ID: "wecom"}, Dir: dir, CLI: map[string]any{}}
	catalog, err := Load(pkg)
	if err != nil || len(catalog.Operations) != 9 {
		t.Fatal(catalog, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "operations.json")); !os.IsNotExist(err) {
		t.Fatal("package changed")
	}
	op := catalog.Operations[0]
	out, err := projectOutput(*op.CLI, map[string]any{"extra_identity_context": "机器人身份：\n名字：Bot\nID：bot\n授权真人用户身份：\n名字：测试用户  \nID：user-1\nprivate instructions"})
	if err != nil || out["userId"] != "user-1" || out["userName"] != "测试用户" || out["extra_identity_context"] != nil {
		t.Fatal(out, err)
	}
}

func TestWriteReceiptsSurviveServiceRestartAndUnknownOutcome(t *testing.T) {
	var calls atomic.Int32
	upstream := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "send", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, r *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{StructuredContent: map[string]any{"success": true}}, nil
	})
	server := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer server.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0700)
	files := map[string]any{
		"connector.json":  map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []any{map[string]any{"key": "KEY", "type": "password", "required": true}}}},
		"mcp.json":        map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": server.URL}}},
		"operations.json": map[string]any{"version": 1, "operations": []any{map[string]any{"operationId": "send", "effect": "write", "adapter": "mcp", "mcp": map[string]any{"component": "main", "tool": "send"}, "inputSchema": map[string]any{"type": "object"}, "outputSchema": map[string]any{"type": "object", "required": []string{"success"}}}}},
	}
	for name, v := range files {
		raw, _ := json.Marshal(v)
		os.WriteFile(filepath.Join(dir, name), raw, 0600)
	}
	manager := connectorauth.New(context.Background(), sources, nil)
	if _, err := manager.SetToken(context.Background(), "demo", map[string]string{"KEY": "test"}); err != nil {
		t.Fatal(err)
	}
	service := Service{Auth: manager, Sources: sources}
	scope := Scope{Subject: "user", AppID: "workbench", Operations: map[string][]string{"demo": {"send"}}, Check: func() error { return nil }}
	catalog, err := service.Describe(scope, "demo")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{ConnectorID: "demo", OperationID: "send", Revision: catalog.Revision, Arguments: map[string]any{"text": "hello"}, IdempotencyKey: "daily-key-123"}
	if _, err := service.Invoke(context.Background(), scope, req); err == nil || err.Error() != "write_permission_required" || calls.Load() != 0 {
		t.Fatal("read grant wrote", err)
	}
	scope.AllowWrite = true
	req.CredentialRevision = "stale-account"
	if _, err := service.Invoke(context.Background(), scope, req); err == nil || err.Error() != "connector_auth_expired" || calls.Load() != 0 {
		t.Fatal("credential fence", err)
	}
	req.CredentialRevision = ""
	first, err := service.Invoke(context.Background(), scope, req)
	if err != nil || calls.Load() != 1 {
		t.Fatal(first, err, calls.Load())
	}
	service = Service{Auth: manager, Sources: sources}
	second, err := service.Invoke(context.Background(), scope, req)
	if err != nil || calls.Load() != 1 || second.InvocationID != first.InvocationID {
		t.Fatal("duplicate dispatched", second, err)
	}
	req.Arguments["text"] = "changed"
	if _, err := service.Invoke(context.Background(), scope, req); err == nil || err.Error() != "idempotency_conflict" || calls.Load() != 1 {
		t.Fatal("conflicting replay", err)
	}
	req.IdempotencyKey = "unknown-key-123"
	if _, _, err := beginWrite(sources.StateRoot, scope, req); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Invoke(context.Background(), scope, req); err == nil || err.Error() != "invocation_outcome_unknown" || calls.Load() != 1 {
		t.Fatal("unknown replayed", err)
	}
}
