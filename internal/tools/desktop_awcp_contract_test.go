package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestDesktopAwcpSnapshotEmptyParamsMatchesOmission(t *testing.T) {
	invoker := &routingClientRequestInvoker{}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	for _, args := range []map[string]any{
		{"method": desktopAwcpGetSnapshotMethod},
		{"method": desktopAwcpGetSnapshotMethod, "params": map[string]any{}},
	} {
		result, err := executor.invokeDesktopCDP(context.Background(), args, desktopActionTestExecutionContext())
		if err != nil || result.Error != "" || result.ExitCode != 0 {
			t.Fatalf("snapshot rejected: %#v %v", result, err)
		}
	}
	_, requests := invoker.snapshots()
	if len(requests) != 2 || requests[0].Type != desktopAwcpSnapshotAction || requests[1].Type != desktopAwcpSnapshotAction ||
		len(requests[0].Payload) != 0 || !reflect.DeepEqual(requests[0].Payload, requests[1].Payload) {
		t.Fatalf("snapshot forms did not use the same empty wire payload: %#v", requests)
	}
}

func TestDesktopAwcpMalformedSnapshotNeverReachesPage(t *testing.T) {
	for _, raw := range []any{nil, "{}", []any{}, map[string]any{"unexpected": true}} {
		invoker := &routingClientRequestInvoker{}
		executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
		result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{"method": desktopAwcpGetSnapshotMethod, "params": raw}, desktopActionTestExecutionContext())
		_, requests := invoker.snapshots()
		if err != nil || result.Error != "invalid_args" || len(requests) != 0 {
			t.Fatalf("invalid params %#v: result=%#v requests=%#v err=%v", raw, result, requests, err)
		}
	}
}

func TestDesktopAwcpIncidentDiagnosticDoesNotDiscardTarget(t *testing.T) {
	args := map[string]any{"method": desktopAwcpGetSnapshotMethod, "params": map[string]any{}, "targetId": "desktop-f69b015e8acf46cf"}
	err := validateDesktopAwcpCall(args)
	if err == nil || !strings.Contains(err.Error(), "targetId") {
		t.Fatalf("missing actionable diagnostic: %v", err)
	}
	if args["targetId"] != "desktop-f69b015e8acf46cf" {
		t.Fatal("validation silently removed caller target")
	}
}

func TestDesktopAwcpManualProgressiveDisclosure(t *testing.T) {
	// A page may describe a new action without a model-compatible schema or
	// mandatory example. Its complete manual is ordinary tool-result data.
	descriptor := map[string]any{
		"action": "orders.read", "description": "Read an order; consult the detail before invoking.",
		"inputSchema": map[string]any{"oneOf": []any{map[string]any{"type": "object"}}},
		"manual":      "Use id from the selected order; verify result.status afterwards.",
	}
	invoker := &awcpClientRequestInvoker{response: func(request ClientRequest) map[string]any {
		if request.Type != desktopAwcpSnapshotAction || len(request.Payload) != 0 {
			t.Fatalf("manual selector leaked into Desktop wire: %#v", request)
		}
		return map[string]any{"ok": true, "method": desktopAwcpGetSnapshotMethod, "revision": "page-v1", "actions": []any{descriptor}}
	}}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	for _, selected := range []string{"", "orders.read", "orders.missing"} {
		args := map[string]any{"method": desktopAwcpGetSnapshotMethod}
		if selected != "" {
			args["params"] = map[string]any{"action": selected}
		}
		result, err := executor.invokeDesktopCDP(context.Background(), args, desktopActionTestExecutionContext())
		if err != nil {
			t.Fatal(err)
		}
		if selected == "orders.missing" {
			if result.Error != "awcp_action_not_found" {
				t.Fatalf("missing manual: %#v", result)
			}
			continue
		}
		if result.Error != "" {
			t.Fatalf("manual failed: %#v", result)
		}
		response := result.Structured["response"].(map[string]any)
		entry := response["actions"].([]any)[0].(map[string]any)
		if selected == "" {
			if len(entry) != 2 || strings.Contains(result.Output, "oneOf") || strings.Contains(result.Output, "result.status") {
				t.Fatalf("index eagerly exposed detail: %#v", result)
			}
		} else if !reflect.DeepEqual(entry, descriptor) || !strings.Contains(result.Output, "result.status") {
			t.Fatalf("page manual was rewritten: %#v", result)
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
		return map[string]any{"ok": false, "requestId": request.ID, "action": "orders.read", "error": map[string]any{"code": "invalid_arguments", "message": "Consult the page manual"}}
	}}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	// Each explicit model call is executed once. No hidden discovery, automatic
	// retry, failure budget or cross-call disable state exists in the bridge.
	for i := 0; i < 3; i++ {
		result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{"method": desktopAwcpInvokeMethod, "params": params}, desktopActionTestExecutionContext())
		if err != nil || result.ExitCode != -1 || !strings.Contains(result.Output, "Consult the page manual") {
			t.Fatalf("failure lost: %#v %v", result, err)
		}
	}
	if calls != 3 {
		t.Fatalf("unexpected bridge calls: %d", calls)
	}
}
