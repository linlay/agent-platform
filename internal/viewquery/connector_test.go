package viewquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/view"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeInvoker struct {
	scope  connectorops.Scope
	req    connectorops.Request
	result connectorops.Result
	err    error
}

func (f *fakeInvoker) Invoke(_ context.Context, scope connectorops.Scope, req connectorops.Request) (connectorops.Result, error) {
	f.scope, f.req = scope, req
	return f.result, f.err
}

func ordersDocument(query view.Query) view.Document {
	return view.Document{View: view.Reference{Source: "connector", ConnectorID: "crm", Key: "orders"}, Queries: map[string]view.Query{"list": query}}
}

func cliQuery() view.Query {
	var q view.Query
	if err := json.Unmarshal([]byte(`{"adapter":"cli","args":["orders","list","--json",
		{"opt":"--status","param":"status"},{"opt":"--limit","param":"limit"},{"flag":"--archived","param":"archived"},{"param":"id"}],
		"paramsSchema":{"type":"object","properties":{"status":{"type":"string"},"limit":{"type":"integer"},"archived":{"type":"boolean"},"id":{"type":"string"}}}}`), &q); err != nil {
		panic(err)
	}
	return q
}

func TestCLIQueryBuildsArgvWithoutShellAndScopesTheGrant(t *testing.T) {
	invoker := &fakeInvoker{result: connectorops.Result{Stdout: "  {\"items\":[1]}\n"}}
	service := &Service{Connectors: invoker}
	result, err := service.Execute(context.Background(), ordersDocument(cliQuery()), "list",
		json.RawMessage(`{"status":"paid; rm -rf /","limit":20,"archived":true}`), Context{ChatID: "chat-1"})
	if err != nil || string(result.Data) != `{"items":[1]}` || result.ExecutedAt <= 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	want := []string{"orders", "list", "--json", "--status", "paid; rm -rf /", "--limit", "20", "--archived"}
	if !reflect.DeepEqual(invoker.req.Args, want) || invoker.req.ConnectorID != "crm" || invoker.req.Adapter != "cli" {
		t.Fatalf("request %#v", invoker.req)
	}
	if invoker.scope.Subject != "view:chat-1" || !reflect.DeepEqual(invoker.scope.Execution, []connectorops.Permission{{ConnectorID: "crm", Adapter: "cli"}}) {
		t.Fatalf("scope %#v", invoker.scope)
	}
	// false flags and omitted params drop the whole item.
	if _, err := service.Execute(context.Background(), ordersDocument(cliQuery()), "list", json.RawMessage(`{"archived":false}`), Context{}); err != nil ||
		!reflect.DeepEqual(invoker.req.Args, []string{"orders", "list", "--json"}) {
		t.Fatalf("args %#v err %v", invoker.req.Args, err)
	}
}

func TestCLIQueryRejectsOptionLikePositionalsAndNonJSONOutput(t *testing.T) {
	invoker := &fakeInvoker{}
	service := &Service{Connectors: invoker}
	if _, err := service.Execute(context.Background(), ordersDocument(cliQuery()), "list", json.RawMessage(`{"id":"--delete"}`), Context{}); !errors.Is(err, view.ErrDataInvalid) {
		t.Fatalf("option-like positional: %v", err)
	}
	invoker.result = connectorops.Result{Stdout: "ID  STATUS\nA1  open\n"}
	if _, err := service.Execute(context.Background(), ordersDocument(cliQuery()), "list", nil, Context{}); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("text output: %v", err)
	}
	invoker.result = connectorops.Result{ExitCode: 2, Stderr: "error: unknown flag --limit\nusage: …"}
	if _, err := service.Execute(context.Background(), ordersDocument(cliQuery()), "list", nil, Context{}); !errors.Is(err, ErrFailed) || !strings.HasSuffix(err.Error(), "error: unknown flag --limit") {
		t.Fatalf("exit failure: %v", err)
	}
	invoker.err = &connectorops.Error{Code: "connector_auth_required", Status: 401}
	var connectorErr *connectorops.Error
	if _, err := service.Execute(context.Background(), ordersDocument(cliQuery()), "list", nil, Context{}); !errors.As(err, &connectorErr) || connectorErr.Code != "connector_auth_required" {
		t.Fatalf("connector failure passthrough: %v", err)
	}
	if _, err := (&Service{}).Execute(context.Background(), ordersDocument(cliQuery()), "list", nil, Context{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no invoker: %v", err)
	}
}

func TestMCPQueryMergesFixedArgumentsAndNormalizesResults(t *testing.T) {
	query := view.Query{Adapter: "mcp", Component: "crmapi", ToolName: "get_order", Arguments: map[string]any{"includeItems": true}}
	invoker := &fakeInvoker{}
	service := &Service{Connectors: invoker}
	for _, tc := range []struct{ raw, want string }{
		{`{"structuredContent":{"id":"A1"},"content":[{"type":"text","text":"ignored"}]}`, `{"id":"A1"}`},
		{`{"content":[{"type":"text","text":" [1,2] "}]}`, `[1,2]`},
		{`{"content":[{"type":"text","text":"plain words"}]}`, `{"text":"plain words"}`},
	} {
		invoker.result = connectorops.Result{MCP: json.RawMessage(tc.raw)}
		result, err := service.Execute(context.Background(), ordersDocument(query), "list", json.RawMessage(`{"id":"A1"}`), Context{})
		if err != nil || string(result.Data) != tc.want {
			t.Fatalf("%s: %s %v", tc.raw, result.Data, err)
		}
	}
	if !reflect.DeepEqual(invoker.req.Arguments, map[string]any{"includeItems": true, "id": "A1"}) || invoker.req.Component != "crmapi" || invoker.req.ToolName != "get_order" {
		t.Fatalf("request %#v", invoker.req)
	}
	if _, err := service.Execute(context.Background(), ordersDocument(query), "list", json.RawMessage(`{"includeItems":false}`), Context{}); !errors.Is(err, view.ErrDataInvalid) {
		t.Fatalf("fixed argument override: %v", err)
	}
	invoker.result = connectorops.Result{MCP: json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"order not found"}]}`)}
	if _, err := service.Execute(context.Background(), ordersDocument(query), "list", nil, Context{}); !errors.Is(err, ErrFailed) || !strings.Contains(err.Error(), "order not found") {
		t.Fatalf("tool error: %v", err)
	}
}

// An external package's view.json query runs through the real connector
// execution path, the same one native views share in shape.
func TestExternalMCPViewQueryRunsThroughConnectorExecution(t *testing.T) {
	upstream := sdk.NewServer(&sdk.Implementation{Name: "crm", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{Name: "list_orders", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, r *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]any
		_ = json.Unmarshal(r.Params.Arguments, &args)
		return &sdk.CallToolResult{StructuredContent: map[string]any{"items": []any{}, "status": args["status"], "source": args["source"]}}, nil
	})
	server := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer server.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "crm")
	if err := os.MkdirAll(filepath.Join(dir, "views"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]any{
		"connector.json": map[string]any{"id": "crm", "name": "CRM", "version": "1.0.0", "type": "mcp", "authMode": "none"},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"crmapi": map[string]any{"type": "streamableHttp", "url": server.URL}}},
		"view.json": map[string]any{"views": map[string]any{"orders": map[string]any{
			"description": "Orders.", "renderer": "html", "usage": []string{"display"}, "entry": "views/orders.html",
			"queries": map[string]any{"list": map[string]any{"adapter": "mcp", "component": "crmapi", "toolName": "list_orders",
				"arguments":    map[string]any{"source": "view"},
				"paramsSchema": map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string"}}}}},
		}}},
	}
	for name, value := range files {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "views", "orders.html"), []byte("<p>orders</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("crm")
	if err != nil {
		t.Fatal(err)
	}
	doc := view.Document{View: view.Reference{Source: "connector", ConnectorID: "crm", Key: "orders"}, Queries: pkg.Views["orders"].Queries}
	service := &Service{Connectors: &connectorops.Service{Auth: connectorauth.New(context.Background(), sources, nil), Sources: sources}}
	result, err := service.Execute(context.Background(), doc, "list", json.RawMessage(`{"status":"open"}`), Context{ChatID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != `{"items":[],"source":"view","status":"open"}` {
		t.Fatalf("data %s", result.Data)
	}
}
