package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestDesktopCDPReportsFullDesktopValidationThroughReverseBridge(t *testing.T) {
	code := 400
	data := json.RawMessage(`{"ok":false,"method":"Input.dispatchMouseEvent","error":{"code":"invalid_args","message":"3 invalid parameters","details":{"method":"Input.dispatchMouseEvent","executed":false,"retryable":false,"recovery":"Correct params.x, params.y and params.clickCount; do not reload.","issues":[{"path":"params.x","expected":"number","actualType":"string","actualValue":"646"},{"path":"params.y","expected":"number","actualType":"string","actualValue":"344"},{"path":"params.clickCount","expected":"integer","actualType":"string","actualValue":"1"}],"params":{"secret":"hidden"},"webContentsId":42}}}`)
	invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{Frame: "error", Type: "invalid_args", ID: "bad-cdp", Code: &code, Msg: "3 invalid parameters", Data: data}}}
	executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
	result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{
		"requestId": "bad-cdp", "method": "Input.dispatchMouseEvent", "surfaceId": "desktop-test",
		"params": map[string]any{"type": "mousePressed", "x": "646", "y": "344", "button": "left", "clickCount": "1"},
	}, desktopActionTestExecutionContext())
	if err != nil || result.ExitCode != -1 || result.Error != "desktop_cdp_client_rejected" {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
	details := result.Structured["details"].(map[string]any)
	if details["clientErrorType"] != "invalid_args" || details["method"] != "Input.dispatchMouseEvent" || details["executed"] != false || details["retryable"] != false {
		t.Fatalf("lost diagnostics: %#v", details)
	}
	if len(details["issues"].([]any)) != 3 || !strings.Contains(result.Output, "params.clickCount") {
		t.Fatalf("lost issues: %s", result.Output)
	}
	if strings.Contains(result.Output, "hidden") || strings.Contains(result.Output, "webContentsId") {
		t.Fatalf("private data leaked: %s", result.Output)
	}
}

func TestDesktopCDPExceptionFailsToolAndRetainsRawResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		failed     bool
	}{
		{"syntax", `{"ok":true,"method":"Runtime.evaluate","result":{"exceptionDetails":{"text":"Uncaught","lineNumber":0,"columnNumber":594,"exception":{"className":"SyntaxError","description":"SyntaxError: missing ) after argument list"}}}}`, true},
		{"runtime", `{"ok":true,"method":"Runtime.evaluate","result":{"exceptionDetails":{"text":"Uncaught","lineNumber":2,"columnNumber":5,"exception":{"description":"TypeError: missing element"}}}}`, true},
		{"business false", `{"ok":true,"method":"Runtime.evaluate","result":{"result":{"type":"object","value":{"ok":false,"value":""}}}}`, false},
		{"ordinary", `{"ok":true,"method":"Runtime.evaluate","result":{"result":{"type":"number","value":2}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := 0
			invoker := &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{Frame: "response", Type: desktopCDPRequestType, ID: "evaluate", Code: &code, Data: json.RawMessage(tc.body)}}}
			executor := &RuntimeToolExecutor{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, clientRequest: invoker, clientTargets: emptyRunClientTargetStore{}}
			result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{"requestId": "evaluate", "method": "Runtime.evaluate", "params": map[string]any{"expression": "test"}}, desktopActionTestExecutionContext())
			if err != nil {
				t.Fatal(err)
			}
			if (result.ExitCode == -1) != tc.failed {
				t.Fatalf("unexpected result: %#v", result)
			}
			if result.Structured["response"] == nil {
				t.Fatal("raw response lost")
			}
			if tc.failed {
				if result.Error != "desktop_cdp_evaluation_failed" || !strings.Contains(result.Output, "zero-based") || !strings.Contains(result.Output, "side effects") {
					t.Fatalf("missing exception context: %s", result.Output)
				}
				if strings.Contains(result.Output, `"executed":false`) {
					t.Fatal("execution status invented")
				}
			}
		})
	}
}

func TestDesktopCDPInvalidParamsObjectDoesNotSend(t *testing.T) {
	for _, raw := range []any{nil, "{}", []any{}, true, float64(1)} {
		executor, execCtx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
		result, err := executor.invokeDesktopCDP(context.Background(), map[string]any{"method": "Page.reload", "params": raw}, execCtx)
		_, requests := invoker.snapshots()
		if err != nil || result.Error != "invalid_args" || len(requests) != 0 {
			t.Fatalf("invalid object sent: %#v %v", result, err)
		}
	}
}

func TestDesktopCDPDiagnosticsBoundAndPreserveTimeout(t *testing.T) {
	details := map[string]any{}
	appendDesktopCDPDiagnostics(details, json.RawMessage(`{"method":"Runtime.evaluate","error":{"code":"target_timeout","details":{"surfaceId":"desktop-test","containerId":"site:test","timeoutMs":12000,"elapsedMs":12001,"url":"private-url","webContentsId":42}}}`))
	if details["timeoutMs"] != float64(12000) || details["surfaceId"] != "desktop-test" {
		t.Fatalf("timeout missing: %#v", details)
	}
	if _, ok := details["executed"]; ok {
		t.Fatal("timeout must not claim non-execution")
	}
	raw := map[string]any{"issues": []any{map[string]any{"path": "params.x", "expected": "number", "actualType": "object", "actualValue": map[string]any{"secret": "hidden"}}}}
	copyDesktopCDPDiagnosticFields(details, raw)
	data, _ := json.Marshal(details)
	if strings.Contains(string(data), "hidden") || strings.Contains(string(data), "private-url") {
		t.Fatal(string(data))
	}
}
