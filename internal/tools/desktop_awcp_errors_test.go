package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestDesktopAwcpPreflightEvidenceIsScopedAndSanitized(t *testing.T) {
	code := 400
	data := map[string]any{
		"stage": "desktop_preflight", "executionStarted": false, "reason": "page_changed", "secret": "must-not-pass",
	}
	raw, _ := json.Marshal(data)
	frame := contracts.ClientResponseFrame{Frame: "error", Type: "awcp_preflight_rejected", Code: &code, Data: raw}
	details := desktopAwcpRejectionDetails(desktopAwcpInvokeAction, frame)
	if details["executionStarted"] != false || details["stage"] != "desktop_preflight" || details["secret"] != nil {
		t.Fatalf("wrong execution evidence: %#v", details)
	}
	if details["reason"] != "page_changed" {
		t.Fatalf("preflight reason was not preserved: %#v", details)
	}
	if got := desktopAwcpRejectionDetails(desktopCDPRequestType, frame); !reflect.DeepEqual(got, desktopClientRejectionDetails(frame)) {
		t.Fatalf("ordinary CDP projection changed: %#v", got)
	}
	frame.Type = "awcp_transport_failed"
	if got := desktopAwcpRejectionDetails(desktopAwcpInvokeAction, frame); got["executionStarted"] != nil {
		t.Fatalf("non-preflight error forged evidence: %#v", got)
	}
}

func TestDesktopAwcpWirePreflightProofReachesToolResult(t *testing.T) {
	// This is the actual flat AGW error.data shape emitted and asserted by
	// Desktop realtime-broker.test.mjs, not ordinary CDP's metadata wrapper.
	code := 400
	frame := contracts.ClientResponseFrame{Frame: "error", Type: "awcp_preflight_rejected", Code: &code,
		Data: json.RawMessage(`{"stage":"desktop_preflight","executionStarted":false,"reason":"manual_required"}`)}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop},
		clientRequest: &scriptedClientRequestInvoker{frames: []contracts.ClientResponseFrame{frame}}, clientTargets: emptyRunClientTargetStore{}}
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"method": desktopAwcpInvokeMethod, "params": map[string]any{"revision": "revision-a", "action": "orders.read", "args": map[string]any{}},
	}, desktopActionTestExecutionContext())
	if err != nil || result.Error != "desktop_cdp_client_rejected" || result.ExitCode == 0 {
		t.Fatalf("wire rejection lost: %#v %v", result, err)
	}
	details := result.Structured["details"].(map[string]any)
	if details["clientErrorType"] != "awcp_preflight_rejected" || details["stage"] != "desktop_preflight" ||
		details["executionStarted"] != false || details["reason"] != "manual_required" {
		t.Fatalf("wire proof did not reach LLM-facing result: %#v", result)
	}
	if !strings.Contains(result.Output, `"reason":"manual_required"`) {
		t.Fatalf("preflight reason missing from model-visible error: %s", result.Output)
	}
}
