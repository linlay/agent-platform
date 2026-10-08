package llm

import (
	"encoding/json"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/resources"
)

func TestAutomationManageRemainingRunsSchemaSurvivesProtocols(t *testing.T) {
	data, err := resources.ToolFS.ReadFile("tools/automation_manage.yml")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	root := contracts.AnyMapNode(tree)
	specs := toOpenAIToolSpecs([]api.ToolDetailResponse{{Name: "automation_manage", Parameters: contracts.AnyMapNode(root["inputSchema"])}})
	for protocol, schema := range map[string]any{
		"openai":    specs[0].Function.Parameters,
		"anthropic": toAnthropicToolSpecs(specs)[0]["input_schema"],
	} {
		t.Run(protocol, func(t *testing.T) {
			raw, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			properties := contracts.AnyMapNode(wire["properties"])
			args := contracts.AnyMapNode(properties["args"])
			fields := contracts.AnyMapNode(args["properties"])
			remaining := contracts.AnyMapNode(fields["remainingRuns"])
			if !reflect.DeepEqual(remaining["type"], []any{"integer", "null"}) || remaining["minimum"] != float64(1) || remaining["maximum"] != float64(100) {
				t.Fatalf("remainingRuns must allow integer or null with bounds 1..100, got %s", raw)
			}
			required, _ := args["required"].([]any)
			for _, name := range required {
				if name == "remainingRuns" {
					t.Fatal("remainingRuns must remain optional so omission preserves the existing limit")
				}
			}
		})
	}
}
