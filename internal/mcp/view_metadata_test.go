package mcp

import (
	"context"
	"encoding/json"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRemoteInvalidViewDoesNotDisableTools(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "views", Version: "1"}, nil)
	for name, meta := range map[string]sdk.Meta{
		"healthy": {},
		"legacy":  {"viewportKey": "question"},
		"invalid": {"view": map[string]any{"key": "../bad"}},
		"valid":   {"view": map[string]any{"connectorId": "cards", "key": "summary"}},
	} {
		server.AddTool(&sdk.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`), Meta: meta}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	}
	endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer endpoint.Close()
	root := t.TempDir()
	if err := writeConnectorFixture(filepath.Join(root, "server.yml"), []byte("key: demo\nbaseUrl: "+endpoint.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	gate := NewAvailabilityGate()
	client := NewClientWithGate(registry, endpoint.Client(), gate)
	defer client.Close()
	for attempt := 0; attempt < 2; attempt++ {
		defs, err := client.ListTools(t.Context(), "demo")
		if err != nil || len(defs) != 4 {
			t.Fatalf("list: %#v %v", defs, err)
		}
		for _, def := range defs {
			meta := def.ToAPITool("demo").Meta
			bad := def.Name == "legacy" || def.Name == "invalid"
			if (meta["viewError"] == "invalid_view") != bad {
				t.Fatalf("metadata: %#v", def)
			}
			if bad && (def.View != nil || meta["view"] != nil || meta["viewportKey"] != nil) {
				t.Fatalf("invalid view retained: %#v", meta)
			}
			if def.Name == "valid" && def.View == nil {
				t.Fatal("valid view lost")
			}
			if _, err := client.CallTool(t.Context(), "demo", def.Name, map[string]any{}, nil); err != nil {
				t.Fatalf("call %s: %v", def.Name, err)
			}
		}
		if gate.IsBlocked("demo") {
			t.Fatal("presentation metadata tripped availability")
		}
	}
}
