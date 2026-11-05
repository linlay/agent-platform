package llm

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/resources"
)

func TestChatStartSchemaOptionalFieldsSurviveProtocols(t *testing.T) {
	data, err := resources.ToolFS.ReadFile("tools/chat_send.yml")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	root := tree.(map[string]any)
	schema := root["inputSchema"].(map[string]any)
	specs := toOpenAIToolSpecs([]api.ToolDetailResponse{{Name: "chat_send", Parameters: schema}})
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
		if _, ok := properties["teamId"]; ok {
			t.Fatal("chat_send still advertises teamId")
		}
		if _, ok := properties["agentKey"]; !ok {
			t.Fatal("chat_send is missing optional agentKey")
		}

		enum, ok := properties["accessLevel"].(map[string]any)["enum"].([]any)
		if !ok || len(enum) != 4 {
			t.Fatalf("enum changed: %s", raw)
		}
	}
}
