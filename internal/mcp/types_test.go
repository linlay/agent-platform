package mcp

import (
	"agent-platform/internal/view"
	"encoding/json"
	"testing"
)

func TestToolDefinitionUnmarshalJSONSupportsViewportType(t *testing.T) {
	var tool ToolDefinition
	if err := json.Unmarshal([]byte(`{"name":"ask","view": {"key": "confirm_dialog"}}`), &tool); err != nil {
		t.Fatalf("unmarshal tool definition: %v", err)
	}
	if tool.View.Key != "confirm_dialog" {
		t.Fatalf("expected viewportType builtin, got %#v", tool)
	}
}

func TestToolDefinitionToAPIToolUsesViewportTypeMeta(t *testing.T) {
	tool := ToolDefinition{
		Name: "ask",
		View: view.Builtin("confirm_dialog"),
	}
	apiTool := tool.ToAPITool("demo")
	if apiTool.Meta["view"].(map[string]any)["key"] != "confirm_dialog" {
		t.Fatalf("expected viewportType meta, got %#v", apiTool.Meta)
	}
	if apiTool.Meta["view"].(map[string]any)["key"] != "confirm_dialog" {
		t.Fatalf("expected viewportKey meta, got %#v", apiTool.Meta)
	}
	for _, field := range []string{"type", "kind", "toolAction", "submitResultFormat"} {
		if _, exists := apiTool.Meta[field]; exists {
			t.Fatalf("did not expect %s meta, got %#v", field, apiTool.Meta)
		}
	}
}

func TestToolDefinitionMapsReadOnlyAnnotationToMeta(t *testing.T) {
	var tool ToolDefinition
	if err := json.Unmarshal([]byte(`{"name":"lookup","annotations":{"readOnlyHint":true}}`), &tool); err != nil {
		t.Fatalf("unmarshal tool definition: %v", err)
	}
	apiTool := tool.ToAPITool("demo")
	if apiTool.Meta["readOnly"] != true {
		t.Fatalf("expected readOnly metadata, got %#v", apiTool.Meta)
	}
}

func TestToolDefinitionPublishesOptionalOutputSchema(t *testing.T) {
	var tool ToolDefinition
	if err := json.Unmarshal([]byte(`{
  "name":"timeline",
  "inputSchema":{"type":"object"},
  "outputSchema":{"type":"object","properties":{"event":{"x-platform-time":"epoch-ms"}}}
}`), &tool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	apiTool := tool.ToAPITool("demo")
	properties, _ := apiTool.OutputSchema["properties"].(map[string]any)
	event, _ := properties["event"].(map[string]any)
	if event["x-platform-time"] != "epoch-ms" {
		t.Fatalf("output schema not preserved: %#v", apiTool.OutputSchema)
	}
}
