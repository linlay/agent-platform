package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
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
	err := ValidateDesktopAwcpCall(args)
	if err == nil || !strings.Contains(err.Error(), "targetId") || !strings.Contains(err.Error(), `{"method":"AWCP.getSnapshot"}`) {
		t.Fatalf("missing actionable diagnostic: %v", err)
	}
	if args["targetId"] != "desktop-f69b015e8acf46cf" {
		t.Fatal("validation silently removed caller target")
	}
}

func TestDesktopAwcpSingleActionEnvelope(t *testing.T) {
	for _, input := range []map[string]any{
		{}, {"nested": map[string]any{"list": []any{true, float64(3), "x"}}},
	} {
		args := map[string]any{"method": desktopAwcpInvokeMethod, "params": map[string]any{"action": map[string]any{"orders.read": input}}}
		if err := ValidateDesktopAwcpCall(args); err != nil {
			t.Fatal(err)
		}
		name, actual, err := DesktopAwcpInvocation(args)
		if err != nil || name != "orders.read" || !reflect.DeepEqual(actual, input) {
			t.Fatal("input altered")
		}
	}
	for _, params := range []any{
		"", nil, []any{}, map[string]any{},
		map[string]any{"action": "orders.read"},
		map[string]any{"action": map[string]any{}},
		map[string]any{"action": map[string]any{"orders.read": map[string]any{}, "orders.write": map[string]any{}}},
		map[string]any{"action": map[string]any{"Orders.read": map[string]any{}}},
		map[string]any{"action": map[string]any{"orders.read": ""}},
		map[string]any{"action": map[string]any{"orders.read": []any{}}},
		map[string]any{"action": map[string]any{"orders.read": map[string]any{}}, "revision": "forged"},
		map[string]any{"revision": "old", "action": "orders.read", "args": map[string]any{}},
	} {
		args := map[string]any{"method": desktopAwcpInvokeMethod, "params": params}
		if err := ValidateDesktopAwcpCall(args); err == nil {
			t.Fatalf("accepted malformed params %#v", params)
		}
	}
}

func TestDesktopAwcpCannotInvokeWithoutTrustedBinding(t *testing.T) {
	invoker := &routingClientRequestInvoker{}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	args := map[string]any{"method": desktopAwcpInvokeMethod, "params": map[string]any{"action": map[string]any{"orders.read": map[string]any{}}}}
	result, err := executor.invokeDesktopCDP(context.Background(), args, desktopActionTestExecutionContext())
	_, requests := invoker.snapshots()
	if err != nil || result.Error != "awcp_request_binding_missing" || len(requests) != 0 {
		t.Fatalf("unbound call reached transport: %#v %v", result, err)
	}
}
