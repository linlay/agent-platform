package llm

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/models"
)

func TestAwcpCompleteActionSchemaReachesFinalProviderRequests(t *testing.T) {
	s := awcpTestStream()
	schema := map[string]any{
		"type": "object", "required": []any{"value"}, "additionalProperties": false,
		"properties": map[string]any{"value": map[string]any{"type": "object", "anyOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			map[string]any{"$ref": "#/$defs/condition"},
		}}},
		"$defs": map[string]any{"condition": map[string]any{"type": "object", "required": []any{"field", "numberValue"}, "additionalProperties": false,
			"properties": map[string]any{"field": map[string]any{"type": "string", "enum": []any{"amount"}}, "numberValue": map[string]any{"type": "number"}}}},
	}
	s.applyAwcpConstraint("revision-7", []awcpActionConstraint{{action: "orders.replace", description: "Replace", example: map[string]any{"value": map[string]any{}}, inputSchema: schema}})
	specs, binding, err := s.awcpRequestTools()
	if err != nil || binding == nil || binding.revision != "revision-7" {
		t.Fatalf("dynamic binding failed: %v", err)
	}
	for _, protocol := range []struct {
		name      string
		adapter   providerProtocol
		schemaKey string
	}{
		{"openai", &openAIProtocol{}, "parameters"}, {"anthropic", &anthropicProtocol{}, "input_schema"},
	} {
		request, err := protocol.adapter.PrepareRequest(protocolStreamParams{
			provider:  models.ProviderDefinition{Key: "test", BaseURL: "https://provider.example.test", APIKey: "test"},
			model:     models.ModelDefinition{ModelID: "test-model"},
			messages:  []openAIMessage{{Role: "user", Content: "Replace condition"}},
			toolSpecs: specs, toolChoice: "auto",
		})
		if err != nil {
			t.Fatalf("%s request failed: %v", protocol.name, err)
		}
		var body map[string]any
		if err := json.Unmarshal(request.RequestBodyJSON, &body); err != nil {
			t.Fatal(err)
		}
		items, ok := body["tools"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("%s did not send exactly one tool", protocol.name)
		}
		tool := anyMap(items[0])
		if protocol.name == "openai" {
			tool = anyMap(tool["function"])
		}
		if tool["name"] != desktopCdpToolName {
			t.Fatalf("%s tool changed name", protocol.name)
		}
		root := anyMap(tool[protocol.schemaKey])
		params := anyMap(anyMap(root["properties"])["params"])
		action := anyMap(anyMap(anyMap(params["properties"])["action"])["properties"])
		input := anyMap(action["orders.replace"])
		if input["required"] == nil || input["additionalProperties"] != false {
			t.Fatalf("%s stripped input constraints", protocol.name)
		}
		value := anyMap(anyMap(input["properties"])["value"])
		branches, ok := value["anyOf"].([]any)
		if !ok || len(branches) != 2 || anyMap(branches[1])["$ref"] != "#/properties/params/properties/action/properties/orders.replace/$defs/condition" {
			t.Fatalf("%s stripped anyOf or local reference: %#v", protocol.name, value)
		}
	}
}
