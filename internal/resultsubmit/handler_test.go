package resultsubmit

import (
	"context"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

var testSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary": map[string]any{"type": "string"},
		"report":  map[string]any{"type": "string"},
	},
	"required":             []any{"summary"},
	"additionalProperties": false,
}

func TestCompileOutputSchemaRequiresObjectRoot(t *testing.T) {
	if _, err := CompileOutputSchema(testSchema); err != nil {
		t.Fatal(err)
	}
	for name, schema := range map[string]map[string]any{
		"empty":   nil,
		"array":   {"type": "array"},
		"no type": {"properties": map[string]any{}},
		"broken":  {"type": "object", "properties": map[string]any{"a": map[string]any{"type": 7}}},
	} {
		if _, err := CompileOutputSchema(schema); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildToolDefinitionLeavesEmbeddedDefinitionUnchanged(t *testing.T) {
	base := api.ToolDetailResponse{Name: ToolName, Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{"type": "string"},
			"output": map[string]any{"type": "object", "description": "Required on success."},
		},
	}}
	definition, err := BuildToolDefinition(base, testSchema)
	if err != nil {
		t.Fatal(err)
	}
	output := definition.Parameters["properties"].(map[string]any)["output"].(map[string]any)
	if _, ok := output["properties"].(map[string]any)["summary"]; !ok || output["description"] != "Required on success." {
		t.Fatalf("output format not applied: %#v", output)
	}
	if _, changed := base.Parameters["properties"].(map[string]any)["output"].(map[string]any)["properties"]; changed {
		t.Fatal("embedded definition was modified")
	}
	if _, changed := testSchema["description"]; changed {
		t.Fatal("caller schema was modified")
	}
	if _, err := BuildToolDefinition(api.ToolDetailResponse{Name: "other"}, testSchema); err == nil {
		t.Fatal("built from another tool")
	}
}

func invoke(t *testing.T, session contracts.QuerySession, args map[string]any) contracts.ToolExecutionResult {
	t.Helper()
	result, err := NewToolHandler().Invoke(context.Background(), ToolName, args, &contracts.ExecutionContext{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSubmissionShape(t *testing.T) {
	session := contracts.QuerySession{}
	for name, args := range map[string]map[string]any{
		"no status":         {},
		"bad status":        {"status": "done"},
		"success no output": {"status": "success"},
		"success reason":    {"status": "success", "output": map[string]any{}, "reason": "x"},
		"failure output":    {"status": "failure", "reason": "x", "output": map[string]any{}},
		"failure blank":     {"status": "failure", "reason": "  "},
		"unknown field":     {"status": "failure", "reason": "x", "note": "y"},
	} {
		if result := invoke(t, session, args); result.ExitCode == 0 || result.Error != CodeInvalid {
			t.Errorf("%s: accepted %#v", name, result)
		}
	}
	// Outside /api/execute the output is not checked against any format.
	if result := invoke(t, session, map[string]any{"status": "success", "output": map[string]any{"any": 1}}); result.ExitCode != 0 {
		t.Fatalf("plain submission rejected: %#v", result)
	}
}

func TestSubmissionAgainstContract(t *testing.T) {
	contract := &contracts.ResultContract{OutputSchema: testSchema}
	session := contracts.QuerySession{ResultContract: contract, ChatRoot: t.TempDir()}
	if result := invoke(t, session, map[string]any{"status": "success", "output": map[string]any{"summary": 5}}); result.Error != CodeInvalid {
		t.Fatalf("format violation accepted: %#v", result)
	}
	if contract.Submission() != nil {
		t.Fatal("rejected submission was recorded")
	}
	if result := invoke(t, session, map[string]any{"status": "success", "output": map[string]any{"summary": "ok"}}); result.ExitCode != 0 {
		t.Fatalf("valid submission rejected: %#v", result)
	}
	if submission := contract.Submission(); submission == nil || submission.Output["summary"] != "ok" {
		t.Fatalf("submission not recorded: %#v", submission)
	}
	if result := invoke(t, session, map[string]any{"status": "failure", "reason": "late"}); result.Error != CodeAlreadySubmitted {
		t.Fatalf("second submission accepted: %#v", result)
	}
	if contract.Submission().Status != contracts.ResultStatusSuccess {
		t.Fatal("first submission was replaced")
	}
}
