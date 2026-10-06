package llm

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/resources"
)

func TestRunQuerySchemaOptionalFieldsSurviveProtocols(t *testing.T) {
	data, err := resources.ToolFS.ReadFile("tools/chat_start.yml")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	root := tree.(map[string]any)
	schema := root["inputSchema"].(map[string]any)
	specs := toOpenAIToolSpecs([]api.ToolDetailResponse{{Name: "chat_start", Parameters: schema}})
	assertSerializedToolSchemaRoots(t, "openai", map[string]any{"tools": openAIToolSpecsToAny(specs)}, "parameters")
	assertSerializedToolSchemaRoots(t, "anthropic", map[string]any{"tools": toAnthropicToolSpecs(specs)}, "input_schema")
	for _, payload := range []any{specs[0].Function.Parameters, toAnthropicToolSpecs(specs)[0]["input_schema"]} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		required := result["required"].([]any)
		if len(required) != 1 || required[0] != "message" {
			t.Fatalf("optional fields became required: %s", raw)
		}
		properties := result["properties"].(map[string]any)
		enum, ok := properties["accessLevel"].(map[string]any)["enum"].([]any)
		if !ok || len(enum) != 3 {
			t.Fatalf("enum changed: %s", raw)
		}
	}
}
