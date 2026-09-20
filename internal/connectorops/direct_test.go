package connectorops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []any{map[string]any{"key": "KEY", "type": "password", "required": true}}}},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": server.URL}}},
	}
	for name, v := range files {
		raw, _ := json.Marshal(v)
		os.WriteFile(filepath.Join(dir, name), raw, 0600)
	}
	manager := connectorauth.New(context.Background(), sources, nil).WithCredentialValidator(mcp.NewClientWithGate(nil, nil, nil).ValidateConnectorCredentials)
	if _, err := manager.SetToken(context.Background(), "demo", map[string]string{"KEY": "test"}); err != nil {
		t.Fatal(err)
	}
	service := Service{Auth: manager, Sources: sources}
	scope := Scope{Subject: "user", AppID: "workbench", Execution: []Permission{}, Check: func() error { return nil }}

	req := Request{ConnectorID: "demo", Adapter: "mcp", Component: "main", ToolName: "send", Arguments: map[string]any{"text": "hello"}, IdempotencyKey: "daily-key-123"}
	if _, err := service.Invoke(context.Background(), scope, req); err == nil || err.Error() != "connector_execution_not_allowed" || calls.Load() != 0 {
		t.Fatal("read grant wrote", err)
	}
	scope.Execution = []Permission{{"demo", "mcp"}}
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
