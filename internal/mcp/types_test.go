package mcp

import (
	"encoding/json"
	"testing"
)

func TestToolDefinitionUnmarshalJSONSupportsViewportType(t *testing.T) {
	var tool ToolDefinition
	if err := json.Unmarshal([]byte(`{"name":"ask","viewportType":"builtin","viewportKey":"confirm_dialog"}`), &tool); err != nil {
		t.Fatalf("unmarshal tool definition: %v", err)
	}
	if tool.ViewportType != "builtin" {
		t.Fatalf("expected viewportType builtin, got %#v", tool)
	}
}

func TestToolDefinitionUnmarshalJSONSupportsLegacyToolType(t *testing.T) {
	var tool ToolDefinition
	if err := json.Unmarshal([]byte(`{"name":"ask","toolType":"builtin","viewportKey":"confirm_dialog"}`), &tool); err != nil {
		t.Fatalf("unmarshal legacy tool definition: %v", err)
	}
	if tool.ViewportType != "builtin" {
		t.Fatalf("expected legacy toolType to map to viewportType, got %#v", tool)
	}
}

func TestToolDefinitionToAPIToolUsesViewportTypeMeta(t *testing.T) {
	tool := ToolDefinition{
		Name:         "ask",
		ViewportType: "builtin",
		ViewportKey:  "confirm_dialog",
	}
	apiTool := tool.ToAPITool("demo")
	if apiTool.Meta["viewportType"] != "builtin" {
		t.Fatalf("expected viewportType meta, got %#v", apiTool.Meta)
	}
	if _, exists := apiTool.Meta["toolType"]; exists {
		t.Fatalf("did not expect legacy toolType meta, got %#v", apiTool.Meta)
	}
}
