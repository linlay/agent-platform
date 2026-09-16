package llm

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/tools"
)

func TestAwcpArgumentsPreserveNativeJSONAndRejectUnsafeInputs(t *testing.T) {
	raw := `{"method":"AWCP.invoke","params":{"action":{"orders.replace":{"value":{},"nodes":[],"numberValue":10,"text":"${previousResult.value}"}}}}`
	var args map[string]any
	if err := decodeToolCallArguments(desktopCdpToolName, raw, &args); err != nil {
		t.Fatal(err)
	}
	action, input, err := tools.DesktopAwcpInvocation(args)
	if err != nil || action != "orders.replace" {
		t.Fatalf("envelope changed: %v %q", err, action)
	}
	if _, ok := input["value"].(map[string]any); !ok {
		t.Fatal("empty object changed type")
	}
	if nodes, ok := input["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Fatal("empty array changed type")
	}
	if number, ok := input["numberValue"].(json.Number); !ok || number.String() != "10" {
		t.Fatal("number changed type or value")
	}
	if input["text"] != "${previousResult.value}" {
		t.Fatal("template changed literal AWCP input")
	}
	for _, bad := range []string{
		`{"method":"AWCP.invoke","params":{"action":{"orders.replace":{"numberValue":9007199254740993}}}}`,
		`{"method":"AWCP.invoke","params":{"action":{"orders.replace":{"value":{},"value":[]}}}}`,
		`{"method":"AWCP.invoke","params":{"action":{"orders.replace":{"nodes":[}}}}`,
	} {
		args = nil
		if err := decodeToolCallArguments(desktopCdpToolName, bad, &args); err == nil {
			t.Fatalf("accepted unsafe arguments: %s", bad)
		}
	}
}

func TestAwcpUnchangedInputErrorStopsBeforeAnotherDispatch(t *testing.T) {
	s := awcpFormatBatchTestStream()
	first, _, message := s.prepareToolCall(awcpFormatBatchCall("first", "orders.read", map[string]any{}))
	if first == nil || message != nil {
		t.Fatal("first valid Action was rejected")
	}
	s.observeDesktopToolResult(first, awcpTrustedPreflightResult("input_schema_mismatch"))
	s.runLLMChatCompletionCount++
	repeated, _, failure := s.prepareToolCall(awcpFormatBatchCall("repeated", "orders.read", map[string]any{}))
	if repeated != nil || failure == nil || s.awcpConstraint.stoppedReason != "unchanged_parameter_error" {
		t.Fatalf("unchanged error was dispatched again: %#v %#v", repeated, s.awcpConstraint)
	}
}

func TestAwcpInputFingerprintKeepsNumericValuesAndArrayOrder(t *testing.T) {
	binding := &awcpRequestBinding{revision: "r"}
	makeCall := func(value json.Number, nodes []any) map[string]any {
		return map[string]any{"method": desktopAwcpInvokeMethod, "params": map[string]any{
			"action": map[string]any{"orders.replace": map[string]any{"value": value, "nodes": nodes}},
		}}
	}
	first, ok := awcpInputFingerprint(binding, makeCall(json.Number("1.0000000000000001"), []any{"a", "b"}))
	if !ok {
		t.Fatal("valid native JSON was not fingerprinted")
	}
	same, ok := awcpInputFingerprint(binding, makeCall(json.Number("1.0000000000000001"), []any{"a", "b"}))
	if !ok || first != same {
		t.Fatal("unchanged native JSON produced different fingerprints")
	}
	changedNumber, ok := awcpInputFingerprint(binding, makeCall(json.Number("1.0"), []any{"a", "b"}))
	if !ok || first == changedNumber {
		t.Fatal("different JSON numbers were merged")
	}
	changedOrder, ok := awcpInputFingerprint(binding, makeCall(json.Number("1.0000000000000001"), []any{"b", "a"}))
	if !ok || first == changedOrder {
		t.Fatal("different array order was merged")
	}
}
