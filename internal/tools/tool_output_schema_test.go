package tools

import (
	"context"
	"encoding/json"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
)

type outputSchemaMCPClient struct {
	payload any
}

func (c outputSchemaMCPClient) CallTool(context.Context, string, string, map[string]any, map[string]any) (any, error) {
	return c.payload, nil
}

type outputSchemaToolCatalog struct {
	def api.ToolDetailResponse
}

func (c outputSchemaToolCatalog) Definitions() []api.ToolDetailResponse {
	return []api.ToolDetailResponse{c.def}
}

func (c outputSchemaToolCatalog) Tool(name string) (api.ToolDetailResponse, bool) {
	return c.def, c.def.Name == name
}

func TestDeclaredOutputSchemaLeavesUndeclaredBusinessTimesOpaque(t *testing.T) {
	def := api.ToolDetailResponse{
		Name: "external-tool",
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"declared": map[string]any{"x-platform-time": timecontract.OutputSchemaEpochMillis},
			},
		},
	}
	result := contracts.ToolExecutionResult{Structured: map[string]any{
		"createdAt": "2026-07-14T08:00:00Z",
		"declared":  json.Number("1700000000000"),
	}}
	validated, err := validateDeclaredOutputSchema(def, result, nil)
	if err != nil || validated.Error != "" {
		t.Fatalf("opaque business field should pass: result=%#v err=%v", validated, err)
	}
}

func TestDeclaredOutputSchemaRejectsInvalidEpochMillis(t *testing.T) {
	def := api.ToolDetailResponse{
		Name: "external-tool",
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"published": map[string]any{"x-platform-time": timecontract.OutputSchemaEpochMillis},
			},
		},
	}
	result := contracts.ToolExecutionResult{Structured: map[string]any{"published": "1700000000000"}}
	validated, err := validateDeclaredOutputSchema(def, result, nil)
	if !timecontract.IsViolation(err) {
		t.Fatalf("expected explicit schema violation, got %v", err)
	}
	if validated.Error != "time_contract_violation" || validated.ExitCode != -1 {
		t.Fatalf("unexpected validation result %#v", validated)
	}
}

func TestToolDefinitionLoaderPublishesOutputSchema(t *testing.T) {
	def, err := parseToolDefinition(map[string]any{
		"name":        "schema-tool",
		"inputSchema": map[string]any{"type": "object"},
		"outputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"eventTime": map[string]any{"x-platform-time": timecontract.OutputSchemaEpochMillis}},
		},
	}, toolDefinitionParseOptions{})
	if err != nil {
		t.Fatalf("parse tool definition: %v", err)
	}
	if def.OutputSchema["type"] != "object" {
		t.Fatalf("output schema missing: %#v", def)
	}
}

func TestMCPResultUsesOutputSchemaWithoutGuessingBusinessFieldNames(t *testing.T) {
	base := api.ToolDetailResponse{
		Name: "mcp-result",
		Meta: map[string]any{"sourceType": "mcp", "serverKey": "remote"},
	}
	for _, tc := range []struct {
		name       string
		schema     map[string]any
		structured map[string]any
		wantError  bool
	}{
		{
			name:       "no schema leaves createdAt opaque",
			structured: map[string]any{"createdAt": "2026-07-14T08:00:00Z"},
		},
		{
			name: "explicit epoch rejects string",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"createdAt": map[string]any{"x-platform-time": timecontract.OutputSchemaEpochMillis},
			}},
			structured: map[string]any{"createdAt": "2026-07-14T08:00:00Z"},
			wantError:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := base
			def.OutputSchema = tc.schema
			router := NewToolRouter(
				stubBackendToolExecutor{},
				outputSchemaMCPClient{payload: map[string]any{"structuredContent": tc.structured}},
				outputSchemaToolCatalog{def: def},
				nil,
				nil,
			)
			result, err := router.Invoke(context.Background(), def.Name, nil, nil)
			if tc.wantError {
				if !timecontract.IsViolation(err) || result.Error != "time_contract_violation" {
					t.Fatalf("expected schema violation result, got result=%#v err=%v", result, err)
				}
				return
			}
			if err != nil || result.Error != "" || result.Structured["createdAt"] != "2026-07-14T08:00:00Z" {
				t.Fatalf("opaque MCP result should pass: result=%#v err=%v", result, err)
			}
		})
	}
}
