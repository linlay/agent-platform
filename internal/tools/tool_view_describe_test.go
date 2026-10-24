package tools

import (
	"agent-platform/internal/api"
	"encoding/json"
	"strings"
	"testing"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/view"
)

func TestViewDescribeDisclosesOnlyFrozenMounts(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	var registered []api.ToolDetailResponse
	for _, def := range defs {
		if def.Name == ViewDescribeToolName {
			registered = append(registered, def)
		}
	}
	if len(registered) != 1 || registered[0].Meta["readOnly"] == false {
		t.Fatalf("embedded view_describe = %#v", registered)
	}
	router := mustNewToolRouter(t, stubBackendToolExecutor{defs: registered}, nil, gatewayTestCatalog{}, nil)
	execCtx := &ExecutionContext{Session: QuerySession{AgentKey: "agent", ToolNames: []string{ViewDescribeToolName}, ToolSetFrozen: true, ViewMounts: []view.Mount{{ID: "crm", Views: map[string]view.Definition{
		"orders": {Description: "List orders.", Usage: []string{"display"}, Queries: map[string]view.Query{"list": {Adapter: "cli", Args: []view.QueryArg{{Literal: "orders"}}, ParamsSchema: json.RawMessage(`{"type":"object"}`)}}},
	}}}}}
	result := invokeGateway(t, router, ViewDescribeToolName, map[string]any{"key": "crm/orders"}, execCtx)
	if result.Error != "" || result.Structured["key"] != "crm/orders" || result.Structured["queries"] == nil {
		t.Fatalf("describe = %#v", result)
	}
	// How a query runs stays private; the model only writes params.
	if !strings.Contains(result.Output, "```view key=crm/orders query=list") || strings.Contains(result.Output, "cli") || strings.Contains(result.Output, `<`) {
		t.Fatalf("output = %s", result.Output)
	}
	if result := invokeGateway(t, router, ViewDescribeToolName, map[string]any{"key": "other/orders"}, execCtx); result.Error != "view_not_found" {
		t.Fatalf("unmounted view = %#v", result)
	}
	for _, args := range []map[string]any{{}, {"key": ""}, {"key": 1}, {"key": "crm/orders", "extra": true}} {
		if result := invokeGateway(t, router, ViewDescribeToolName, args, execCtx); result.Error != "invalid_arguments" {
			t.Fatalf("%#v -> %#v", args, result)
		}
	}
}
