package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestDesktopAwcpManualEmptyParamsMatchesOmission(t *testing.T) {
	invoker := &routingClientRequestInvoker{}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	for _, args := range []map[string]any{
		{"method": desktopAwcpGetManualMethod},
		{"method": desktopAwcpGetManualMethod, "params": map[string]any{}},
	} {
		result, err := executor.invokeDesktopCDP(context.Background(), args, desktopActionTestExecutionContext())
		if err != nil || result.Error != "" || result.ExitCode != 0 {
			t.Fatalf("manual rejected: %#v %v", result, err)
		}
	}
	_, requests := invoker.snapshots()
	if len(requests) != 2 || requests[0].Type != desktopAwcpManualAction || requests[1].Type != desktopAwcpManualAction ||
		len(requests[0].Payload) != 0 || !reflect.DeepEqual(requests[0].Payload, requests[1].Payload) {
		t.Fatalf("manual forms did not use the same empty wire payload: %#v", requests)
	}
}

func TestDesktopAwcpMalformedManualNeverReachesPage(t *testing.T) {
	for _, raw := range []any{nil, "{}", []any{}, map[string]any{"unexpected": true}} {
		invoker := &routingClientRequestInvoker{}
		executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
		result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{"method": desktopAwcpGetManualMethod, "params": raw}, desktopActionTestExecutionContext())
		_, requests := invoker.snapshots()
		if err != nil || result.Error != "invalid_args" || len(requests) != 0 {
			t.Fatalf("invalid params %#v: result=%#v requests=%#v err=%v", raw, result, requests, err)
		}
	}
}

func TestDesktopAwcpInvokeInvalidArgsAreStructuredOnce(t *testing.T) {
	tests := []struct {
		name       string
		value      any
		present    bool
		actualType string
	}{
		{name: "empty string", value: "", present: true, actualType: "string"},
		{name: "encoded object string", value: "{}", present: true, actualType: "string"},
		{name: "array", value: []any{}, present: true, actualType: "array"},
		{name: "null", value: nil, present: true, actualType: "null"},
		{name: "missing", actualType: "missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invoker := &routingClientRequestInvoker{}
			executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
			params := map[string]any{"revision": "page-v2", "action": "orders.read"}
			if test.present {
				params["args"] = test.value
			}
			result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
				"method": desktopAwcpInvokeMethod,
				"params": params,
			}, desktopActionTestExecutionContext())
			if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
				t.Fatalf("unexpected invalid-args result: %#v err=%v", result, err)
			}
			_, requests := invoker.snapshots()
			if len(requests) != 0 {
				t.Fatalf("invalid args reached Desktop: %#v", requests)
			}
			details := result.Structured["details"].(map[string]any)
			if details["stage"] != "platform_parse" || details["executionStarted"] != false ||
				details["field"] != "args" || details["actualType"] != test.actualType || details["expectedType"] != "object" ||
				!reflect.DeepEqual(details["path"], []string{"params", "args"}) {
				t.Fatalf("invalid args lost structured diagnostics: %#v", result.Structured)
			}
			errorPayload := result.Structured["error"].(map[string]any)
			message := errorPayload["message"].(string)
			if strings.Contains(message, `"ok":false`) || strings.Contains(message, `"error":{`) {
				t.Fatalf("structured error was embedded in message: %q", message)
			}
			var output map[string]any
			canonical, marshalErr := json.Marshal(result.Structured)
			if json.Unmarshal([]byte(result.Output), &output) != nil || marshalErr != nil || string(canonical) != result.Output {
				t.Fatalf("tool output was not a single encoding of the structured result: output=%s structured=%#v", result.Output, result.Structured)
			}
		})
	}

	if err := validateDesktopAwcpCall(map[string]any{
		"method": desktopAwcpInvokeMethod,
		"params": map[string]any{"revision": "page-v2", "action": "orders.read", "args": map[string]any{}},
	}); err != nil {
		t.Fatalf("valid empty args object was rejected: %v", err)
	}
}

func TestDesktopAwcpRejectsLegacySectionOnlyManualRequest(t *testing.T) {
	invoker := &routingClientRequestInvoker{}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": desktopAwcpGetManualMethod, "params": map[string]any{"section": "orders.read"},
	}, desktopActionTestExecutionContext())
	_, requests := invoker.snapshots()
	if err != nil || result.Error != "invalid_args" || len(requests) != 0 {
		t.Fatalf("legacy request reached Desktop: result=%#v requests=%#v err=%v", result, requests, err)
	}
}

func TestDesktopAwcpIncidentDiagnosticDoesNotDiscardTarget(t *testing.T) {
	args := map[string]any{"method": desktopAwcpGetManualMethod, "params": map[string]any{}, "targetId": "desktop-f69b015e8acf46cf"}
	err := validateDesktopAwcpCall(args)
	if err == nil || !strings.Contains(err.Error(), "targetId") {
		t.Fatalf("missing actionable diagnostic: %v", err)
	}
	if args["targetId"] != "desktop-f69b015e8acf46cf" {
		t.Fatal("validation silently removed caller target")
	}
}

func TestDesktopAwcpManualProgressiveDisclosure(t *testing.T) {
	invoker := &awcpClientRequestInvoker{response: func(request ClientRequest) map[string]any {
		if request.Type != desktopAwcpManualAction {
			t.Fatalf("manual used the wrong wire action: %#v", request)
		}
		if section, ok := request.Payload["section"].(string); ok {
			return map[string]any{"ok": true, "method": desktopAwcpGetManualMethod, "revision": request.Payload["revision"], "section": section,
				"description": "Read orders", "inputSchema": map[string]any{"oneOf": []any{map[string]any{"type": "object"}}}}
		}
		return map[string]any{"ok": true, "method": desktopAwcpGetManualMethod, "revision": "page-v2",
			"site": map[string]any{"name": "Orders", "description": "Order operations"}, "sections": []any{}}
	}}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	for _, selected := range []string{"", "orders.read"} {
		args := map[string]any{"method": desktopAwcpGetManualMethod}
		if selected != "" {
			args["params"] = map[string]any{"section": selected, "revision": "page-v2"}
		}
		result, err := executor.invokeDesktopCDP(context.Background(), args, desktopActionTestExecutionContext())
		if err != nil {
			t.Fatal(err)
		}
		if result.Error != "" {
			t.Fatalf("manual failed: %#v", result)
		}
		expected := map[string]any{}
		if selected != "" {
			expected["section"] = selected
			expected["revision"] = "page-v2"
		}
		if !reflect.DeepEqual(invoker.request.Payload, expected) {
			t.Fatalf("manual payload was rewritten: %#v", invoker.request)
		}
	}
}

func TestDesktopAwcpInvokeUsesManualEnvelopeWithoutRunBinding(t *testing.T) {
	params := map[string]any{"revision": "page-v1", "action": "orders.read", "args": map[string]any{"id": "order-1"}}
	calls := 0
	invoker := &awcpClientRequestInvoker{response: func(request ClientRequest) map[string]any {
		calls++
		if !reflect.DeepEqual(request.Payload, params) {
			t.Fatalf("input rewritten: %#v", request)
		}
		return map[string]any{"ok": false, "requestId": request.ID, "action": "orders.read", "error": map[string]any{
			"code": "invalid_arguments", "message": "Consult the page manual",
			"details": map[string]any{"executionStarted": false, "fieldErrors": []any{map[string]any{"path": []any{"id"}, "messages": []any{"required"}}}},
		}}
	}}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	// Each explicit model call is executed once. No hidden discovery, automatic
	// retry, failure budget or cross-call disable state exists in the bridge.
	for i := 0; i < 3; i++ {
		result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{"method": desktopAwcpInvokeMethod, "params": params}, desktopActionTestExecutionContext())
		if err != nil || result.ExitCode != -1 || !strings.Contains(result.Output, "Consult the page manual") {
			t.Fatalf("failure lost: %#v %v", result, err)
		}
		if _, exists := result.Structured["stage"]; exists {
			t.Fatalf("page-owned validation was mislabeled as execution: %#v", result.Structured)
		}
	}
	if calls != 3 {
		t.Fatalf("unexpected bridge calls: %d", calls)
	}
}
