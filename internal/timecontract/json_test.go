package timecontract

import (
	"encoding/json"
	"testing"
)

func TestValidateOutputSchemaRejectsOnlyDeclaredEpochMillis(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"declared": map[string]any{"x-platform-time": OutputSchemaEpochMillis},
		},
	}
	for _, value := range []any{
		"1700000000000",
		json.Number("1700000000"),
		float64(1_700_000_000_000),
		json.Number("1700000000000.0"),
		int64(0),
	} {
		if err := ValidateOutputSchema(map[string]any{"declared": value}, schema, "tool.result"); !IsViolation(err) {
			t.Fatalf("declared epoch value %#v should fail, got %v", value, err)
		}
	}
	if err := ValidateOutputSchema(map[string]any{
		"declared":  json.Number("1700000000000"),
		"createdAt": "external ISO remains business data",
	}, schema, "tool.result"); err != nil {
		t.Fatalf("declared integer should pass: %v", err)
	}
}

func TestValidateOutputSchemaReadablePairIsExplicit(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"occurred": map[string]any{"x-platform-time": OutputSchemaEpochMillis},
			"display":  map[string]any{"format": "date-time", "x-platform-time-pair": "occurred"},
		},
	}
	valid := map[string]any{
		"occurred": json.Number("1700000000000"),
		"display":  "2023-11-14T22:13:20Z",
	}
	if err := ValidateOutputSchema(valid, schema, "tool.result"); err != nil {
		t.Fatalf("valid explicit pair: %v", err)
	}
	invalid := map[string]any{
		"occurred": json.Number("1700000000000"),
		"display":  "2023-11-14T22:13:21Z",
	}
	if err := ValidateOutputSchema(invalid, schema, "tool.result"); !IsViolation(err) {
		t.Fatalf("mismatched explicit pair should fail: %v", err)
	}
}

func TestValidateOutputSchemaSelectsOneOf(t *testing.T) {
	schema := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{
				"kind": map[string]any{"const": "external"},
			}},
			map[string]any{"type": "object", "properties": map[string]any{
				"kind": map[string]any{"const": "platform"},
				"at":   map[string]any{"x-platform-time": OutputSchemaEpochMillis},
			}},
		},
	}
	if err := ValidateOutputSchema(map[string]any{"kind": "external", "createdAt": "not a platform time"}, schema, "tool.result"); err != nil {
		t.Fatalf("external branch should stay opaque: %v", err)
	}
	if err := ValidateOutputSchema(map[string]any{"kind": "platform", "at": "1700000000000"}, schema, "tool.result"); !IsViolation(err) {
		t.Fatalf("platform branch must validate declared time: %v", err)
	}
}
