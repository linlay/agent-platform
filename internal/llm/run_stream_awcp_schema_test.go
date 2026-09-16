package llm

import (
	"reflect"
	"strings"
	"testing"
)

func recursiveAwcpTestSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"value": map[string]any{"$ref": "#/$defs/node"}},
		"$defs": map[string]any{"node": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"nodes":        map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/node"}},
				"numberValue":  map[string]any{"type": "number"},
				"booleanValue": map[string]any{"type": "boolean"},
				"root":         map[string]any{"$ref": "#"},
			},
		}},
	}
}

func TestAwcpStoppedSchemaRemovesOnlyAwcpMethods(t *testing.T) {
	s := awcpConstrainedTestStream()
	s.toolSpecs = append(s.toolSpecs, openAIToolSpec{Type: "function", Function: openAIToolDefinition{
		Name: "file_read", Parameters: map[string]any{"type": "object"},
	}})
	base := cloneOpenAIToolSpecs(s.toolSpecs)
	s.stopAwcp("correction_limit")
	generated, binding, err := s.awcpRequestTools()
	if err != nil || binding != nil || len(generated) != len(base) {
		t.Fatalf("AWCP stop removed tools or retained a binding: %v", err)
	}
	want := cloneOpenAIToolSpecs(base)
	anyMap(anyMap(want[0].Function.Parameters["properties"])["method"])["enum"] = []any{"Runtime.evaluate", "Target.getCurrentTarget"}
	if !reflect.DeepEqual(generated, want) || !reflect.DeepEqual(s.toolSpecs, base) {
		t.Fatal("AWCP stop altered ordinary schemas or the static tool cache")
	}
	s.observeDesktopToolResult(awcpDiscoveryInvocation(), awcpDiscoveryResult("late", validAwcpTestActions()))
	s.resetAwcpAttemptAfterSuccess(100)
	if s.awcpConstraint.stoppedReason != "correction_limit" || s.awcpConstraint.revision != "" {
		t.Fatal("late discovery or success reopened a stopped AWCP run")
	}
	fresh := awcpTestStream()
	generated, _, err = fresh.awcpRequestTools()
	if err != nil || !reflect.DeepEqual(generated, fresh.toolSpecs) || fresh.awcpRecoveryGate(desktopCdpToolName, map[string]any{"method": desktopAwcpSnapshotMethod}) != nil {
		t.Fatal("AWCP stop leaked into another Run")
	}
}

func TestAwcpSingleToolSchemaIsTypedAndRequestLocal(t *testing.T) {
	s := awcpTestStream()
	s.toolSpecs[0].Function.Parameters["$defs"] = map[string]any{"ordinary": map[string]any{"type": "string"}}
	base := cloneOpenAIToolSpecs(s.toolSpecs)
	schema := recursiveAwcpTestSchema()
	s.applyAwcpConstraint("r1", []awcpActionConstraint{
		{action: "first.replace", description: "First", inputSchema: schema},
		{action: "second.replace", description: "Second", inputSchema: schema},
	})
	generated, binding, err := s.awcpRequestTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != 1 || generated[0].Function.Name != desktopCdpToolName {
		t.Fatal("added a model tool")
	}
	root := generated[0].Function.Parameters
	params := anyMap(anyMap(root["properties"])["params"])
	if params["additionalProperties"] != true || params["required"] != nil {
		t.Fatal("ordinary CDP params restricted")
	}
	action := anyMap(anyMap(params["properties"])["action"])
	if action["minProperties"] != 1 || action["maxProperties"] != 1 || action["required"] != nil || action["additionalProperties"] != false {
		t.Fatal("not a single optional Action key")
	}
	if !reflect.DeepEqual(root["$defs"], base[0].Function.Parameters["$defs"]) {
		t.Fatal("injected root definitions or changed existing CDP definitions")
	}
	for _, key := range []string{"first.replace", "second.replace"} {
		def := anyMap(anyMap(action["properties"])[key])
		if def["type"] != "object" || def["$ref"] != nil || def["additionalProperties"] != false {
			t.Fatal("Action root is not a directly declared input object")
		}
		prefix := "#/properties/params/properties/action/properties/" + key
		if anyMap(anyMap(def["properties"])["value"])["$ref"] != prefix+"/$defs/node" {
			t.Fatal("root input field did not keep its relocated reference")
		}
		props := anyMap(anyMap(anyMap(def["$defs"])["node"])["properties"])
		if anyMap(anyMap(props["nodes"])["items"])["$ref"] != prefix+"/$defs/node" || anyMap(props["root"])["$ref"] != prefix {
			t.Fatal("recursive reference crossed Action boundary")
		}
	}
	if !reflect.DeepEqual(s.toolSpecs, base) || !reflect.DeepEqual(schema, recursiveAwcpTestSchema()) {
		t.Fatal("mutated static cache or provider schema")
	}
	// Remove only the injected fields; the entire ordinary CDP projection must match.
	projection := cloneToolSchemaMap(root)
	delete(anyMap(anyMap(projection["properties"])["params"]), "properties")
	if !reflect.DeepEqual(projection, base[0].Function.Parameters) {
		t.Fatal("ordinary CDP schema changed")
	}
	s.applyAwcpConstraint("r2", nil)
	if binding.revision != "r1" || len(binding.actions) != 2 {
		t.Fatal("request binding changed with latest snapshot")
	}
	s.toolSpecs = nil
	if specs, bound, err := s.awcpRequestTools(); err != nil || len(specs) != 0 || bound != nil {
		t.Fatal("AWCP reenabled disabled tools")
	}
}

func TestAwcpEmptyActionInputIsExplicitAtTheActionKey(t *testing.T) {
	s := awcpTestStream()
	input := map[string]any{
		"type": "object", "properties": map[string]any{}, "additionalProperties": false,
		"description": "No query fields are accepted.",
	}
	s.applyAwcpConstraint("r1", []awcpActionConstraint{{action: "orders.read", description: "Read orders.", inputSchema: input}})
	generated, _, err := s.awcpRequestTools()
	if err != nil {
		t.Fatal(err)
	}
	root := generated[0].Function.Parameters
	params := anyMap(anyMap(root["properties"])["params"])
	properties := anyMap(anyMap(anyMap(params["properties"])["action"])["properties"])
	got := anyMap(properties["orders.read"])
	want := cloneToolSchemaMap(input)
	want["description"] = "Read orders.\nNo query fields are accepted."
	if !reflect.DeepEqual(got, want) || root["$defs"] != nil {
		t.Fatalf("empty Action contract was hidden or changed: %#v", root)
	}
	if input["description"] != "No query fields are accepted." {
		t.Fatal("mutated the page descriptor")
	}
}

func TestAwcpSchemaRelocatesOnlySchemaLocations(t *testing.T) {
	schema := recursiveAwcpTestSchema()
	schema["examples"] = []any{map[string]any{"$ref": "#/$defs/node", "oneOf": "ordinary data"}}
	schema["description"] = "Example #/$defs/node"
	got, err := relocateAwcpInputSchema(schema, "#/$defs/a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["examples"], schema["examples"]) || got["description"] != schema["description"] {
		t.Fatal("rewrote data")
	}
	for _, ref := range []string{"https://example.test/schema", "#anchor", "#/$defs/missing", "#/examples/0"} {
		candidate := cloneToolSchemaMap(schema)
		anyMap(candidate["properties"])["value"] = map[string]any{"$ref": ref}
		if _, err := relocateAwcpInputSchema(candidate, "#/$defs/a"); err == nil {
			t.Fatalf("accepted unsupported ref %s", ref)
		}
	}
}

func TestAwcpSchemaRejectsUntypedOrBranchingInputs(t *testing.T) {
	for _, candidate := range []map[string]any{
		{}, {"type": "array"}, {"type": "object"},
		{"type": []any{"string", "number"}},
		{"type": "string", "oneOf": []any{}}, {"type": "string", "anyOf": []any{}},
		{"type": "string", "allOf": []any{}}, {"type": "string", "if": map[string]any{}},
		{"type": "string", "$id": "local"},
	} {
		if _, err := relocateAwcpInputSchema(candidate, "#/$defs/a"); err == nil {
			t.Fatalf("accepted incompatible input: %#v", candidate)
		}
	}
}

func TestAwcpSchemaRelocatesTypedAnyOfBranchesAndRecursiveReferences(t *testing.T) {
	schema := map[string]any{
		"type": "object", "properties": map[string]any{
			"value": map[string]any{"type": "object", "anyOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"nodes": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/condition"}}}, "required": []any{"nodes"}, "additionalProperties": false},
				map[string]any{"$ref": "#/$defs/condition"},
			}},
		}, "$defs": map[string]any{"condition": map[string]any{"type": "object", "properties": map[string]any{"field": map[string]any{"type": "string", "enum": []any{"amount"}}}, "required": []any{"field"}, "additionalProperties": false}},
	}
	got, err := relocateAwcpInputSchema(schema, "#/properties/params/properties/action/properties/orders.replace")
	if err != nil {
		t.Fatal(err)
	}
	value := anyMap(anyMap(got["properties"])["value"])
	branches := value["anyOf"].([]any)
	items := anyMap(anyMap(anyMap(branches[0])["properties"])["nodes"])["items"]
	if anyMap(items)["$ref"] != "#/properties/params/properties/action/properties/orders.replace/$defs/condition" ||
		anyMap(branches[1])["$ref"] != "#/properties/params/properties/action/properties/orders.replace/$defs/condition" {
		t.Fatalf("anyOf lost typed recursive references: %#v", branches)
	}
}

func TestAwcpPreparedInvocationCannotBorrowANewerBinding(t *testing.T) {
	s := awcpPreparationTestStream()
	s.applyAwcpConstraint("r", []awcpActionConstraint{{action: "orders.read", description: "Read", inputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}})
	_, s.awcpRequest, _ = s.awcpRequestTools()
	call := openAIToolCall{ID: "bound", Function: openAIFunctionCall{Name: desktopCdpToolName, Arguments: `{"method":"AWCP.invoke","params":{"action":{"orders.read":{}}}}`}}
	invocation, _, _ := s.prepareToolCall(call)
	if invocation == nil {
		t.Fatal("valid request rejected")
	}
	ctx := s.serialExecutionContext(invocation)
	if ctx.DesktopAwcpRevision != "r" || s.execCtx.DesktopAwcpRevision != "" {
		t.Fatal("binding not isolated to invocation")
	}
	s.applyAwcpConstraint("r", s.awcpConstraint.actions)
	if err := s.invokeToolAndPublishResult(invocation); err != nil {
		t.Fatal(err)
	}
	if len(s.engine.tools.(*awcpScriptedExecutor).callNames()) != 0 {
		t.Fatal("stale prepared call executed")
	}
	if s.serialExecutionContext(invocation).DesktopAwcpRevision != "" {
		t.Fatal("stale invocation borrowed current revision")
	}
	if !strings.Contains(s.messages[len(s.messages)-1].Content.(string), "snapshot") {
		t.Fatal("missing binding failure")
	}
	other := awcpPreparationTestStream()
	other.applyAwcpConstraint("r", s.awcpConstraint.actions)
	if err := other.validateAwcpInvocationBinding(invocation); err == nil {
		t.Fatal("cross-run binding accepted")
	}
}
