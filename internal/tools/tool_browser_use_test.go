package tools

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
)

func TestBrowserUseToolsPreserveReverseRequestAndTrustedSource(t *testing.T) {
	for _, tc := range []struct {
		name, tool, method, surfaceID string
		args, params                  map[string]any
	}{
		{"open connected Chrome", "surface_open", "Surface.openChrome", "", map[string]any{"url": " https://example.com/search?q=test "}, map[string]any{"url": "https://example.com/search?q=test"}},
		{"observe", "surface_observe", "Surface.observe", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1"}, nil},
		{"bounded observe", "surface_observe", "Surface.observe", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "maxTextLength": 12000.0, "maxElements": 200.0}, map[string]any{"maxTextLength": 12000.0, "maxElements": 200.0}},
		{"Chrome viewport observe", "surface_observe", "Surface.observe", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "viewportOnly": true}, map[string]any{"viewportOnly": true}},
		{"reference click", "surface_click", "Surface.click", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e2", "observationId": "observation:1", "timeoutMs": 4500.0, "waitFor": map[string]any{"state": "visible", "selector": "#done"}}, map[string]any{"ref": "e2", "observationId": "observation:1", "timeoutMs": 4500.0, "waitFor": map[string]any{"state": "visible", "selector": "#done"}}},
		{"reference fill", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e1", "observationId": "observation:1", "action": "fill", "value": "Ada"}, map[string]any{"ref": "e1", "observationId": "observation:1", "action": "fill", "value": "Ada"}},
		{"reference focus", "surface_element", "Surface.interact", "page:workpanel-1", map[string]any{"surfaceId": "page:workpanel-1", "ref": "e1", "observationId": "observation:2", "action": "focus"}, map[string]any{"ref": "e1", "observationId": "observation:2", "action": "focus"}},
		{"legacy selector click", "surface_click", "Input.click", "page:1", map[string]any{"surfaceId": "page:1", "selector": "button.save"}, map[string]any{"selector": "button.save"}},
		{"legacy coordinate click", "surface_click", "Input.click", "page:1", map[string]any{"surfaceId": "page:1", "x": 120.0, "y": 30.0}, map[string]any{"x": 120.0, "y": 30.0}},
		{"Chrome selector hover bypasses legacy WorkPanel action", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "selector": "button.menu", "action": "hover"}, map[string]any{"selector": "button.menu", "action": "hover"}},
		{"reference type", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e1", "observationId": "o1", "action": "type", "value": "suffix"}, map[string]any{"ref": "e1", "observationId": "o1", "action": "type", "value": "suffix"}},
		{"reference press", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e1", "observationId": "o1", "action": "press", "key": "ControlOrMeta+A"}, map[string]any{"ref": "e1", "observationId": "o1", "action": "press", "key": "ControlOrMeta+A"}},
		{"reference check", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e1", "observationId": "o1", "action": "check"}, map[string]any{"ref": "e1", "observationId": "o1", "action": "check"}},
		{"reference uncheck", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e1", "observationId": "o1", "action": "uncheck"}, map[string]any{"ref": "e1", "observationId": "o1", "action": "uncheck"}},
		{"Chrome select multiple", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "ref": "e1", "observationId": "o1", "action": "select", "values": []any{"a", "b"}}, map[string]any{"ref": "e1", "observationId": "o1", "action": "select", "values": []any{"a", "b"}}},
		{"Chrome clear multiple bypasses legacy WorkPanel action", "surface_element", "Surface.interact", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "selector": "select.tags", "action": "select", "values": []any{}}, map[string]any{"selector": "select.tags", "action": "select", "values": []any{}}},
		{"scroll", "surface_scroll", "Surface.scroll", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "deltaY": 600.0, "deltaX": -30.5}, map[string]any{"deltaY": 600.0, "deltaX": -30.5}},
		{"native dialog", "surface_dialog", "Surface.handleDialog", "page:chrome-1", map[string]any{"surfaceId": "page:chrome-1", "observationId": "o1", "dialogId": "d1", "action": "accept", "promptText": "authorized answer"}, map[string]any{"observationId": "o1", "dialogId": "d1", "action": "accept", "promptText": "authorized answer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, execCtx, invoker := webControlTestRuntime(t, nil)
			result, err := executor.Invoke(context.Background(), tc.tool, tc.args, execCtx)
			if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 {
				t.Fatalf("result=%#v err=%v requests=%#v", result, err, invoker.requests)
			}
			request := invoker.requests[0]
			payload := request.Payload
			if request.Type != desktopCDPRequestType || payload["method"] != tc.method || request.ID == "" || payload["requestId"] != request.ID {
				t.Fatalf("unexpected reverse request: %#v", request)
			}
			if surfaceID, _ := payload["surfaceId"].(string); surfaceID != tc.surfaceID {
				t.Fatalf("surfaceId=%q want %q", surfaceID, tc.surfaceID)
			}
			params, _ := payload["params"].(map[string]any)
			if len(params) != len(tc.params) || len(params) > 0 && !reflect.DeepEqual(params, tc.params) {
				t.Fatalf("params=%#v want %#v", params, tc.params)
			}
			wantSource := map[string]any{"runId": execCtx.Session.RunID, "chatId": execCtx.Session.ChatID, "agentKey": execCtx.Session.AgentKey}
			if !reflect.DeepEqual(payload["source"], wantSource) || request.Source != nil {
				t.Fatalf("trusted source changed: %#v", request)
			}
		})
	}
}

func TestBrowserUseExtendedInputValidationAndNoDispatch(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"surface_observe", map[string]any{"surfaceId": "p", "maxTextLength": 0.0}},
		{"surface_observe", map[string]any{"surfaceId": "p", "maxTextLength": 30001.0}},
		{"surface_observe", map[string]any{"surfaceId": "p", "maxElements": 501.0}},
		{"surface_observe", map[string]any{"surfaceId": "p", "maxElements": "500"}},
		{"surface_observe", map[string]any{"surfaceId": "p", "maxElements": 1.5}},
		{"surface_observe", map[string]any{"surfaceId": "p", "viewportOnly": 1.0}},
		{"surface_scroll", map[string]any{"surfaceId": "p"}},
		{"surface_scroll", map[string]any{"surfaceId": "p", "deltaY": -2001.0}},
		{"surface_scroll", map[string]any{"surfaceId": "p", "deltaY": math.Inf(1)}},
		{"surface_scroll", map[string]any{"surfaceId": "p", "deltaY": 1.0, "deltaX": "1"}},
		{"surface_dialog", map[string]any{"surfaceId": "p", "dialogId": "d1", "action": "accept"}},
		{"surface_dialog", map[string]any{"surfaceId": "p", "observationId": "o1", "dialogId": "d1", "action": "dismiss", "promptText": "no"}},
		{"surface_dialog", map[string]any{"surfaceId": "p", "observationId": "o1", "dialogId": "d1", "action": "auto"}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "input", "action": "press"}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "input", "action": "press", "key": "Meta+L"}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "input", "action": "focus", "key": "Enter"}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "input", "action": "type"}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "input", "action": "check", "value": "true"}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "select", "action": "select", "value": "a", "values": []any{"b"}}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "select", "action": "focus", "values": []any{}}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "select", "action": "select", "values": []any{true}}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "select", "action": "select", "values": nil}},
		{"surface_element", map[string]any{"surfaceId": "p", "selector": "select", "action": "select", "values": []any{strings.Repeat("x", 4097)}}},
	} {
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, execCtx)
		if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
			t.Errorf("%s: result=%#v err=%v", tc.tool, result, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("invalid extended input reached Desktop: %d", len(invoker.requests))
	}
}

func TestBrowserUseActionOutcomeAndNestedObservationArePreserved(t *testing.T) {
	for _, actionResult := range []map[string]any{
		{"outcome": "completed", "observation": map[string]any{"observationId": "o2", "provider": "chrome", "elements": []any{map[string]any{"ref": "e2"}}}},
		{"outcome": "completed", "observationError": map[string]any{"code": "observe_failed", "message": "Read current state."}},
		{"outcome": "unknown", "error": "dialog_pending", "observation": map[string]any{"observationId": "o2", "dialogs": []any{map[string]any{"dialogId": "d1", "type": "confirm", "message": "Continue?"}}}},
	} {
		executor, execCtx, invoker := webControlTestRuntime(t, func(ClientRequest) map[string]any { return map[string]any{"ok": true, "result": actionResult} })
		result, err := executor.Invoke(context.Background(), "surface_element", map[string]any{"surfaceId": "p", "ref": "e1", "observationId": "o1", "action": "fill", "value": "x"}, execCtx)
		response, _ := result.Structured["response"].(map[string]any)
		if err != nil || result.ExitCode != 0 || !reflect.DeepEqual(response["result"], actionResult) || len(invoker.requests) != 1 {
			t.Fatalf("action outcome or observation changed/replayed: %#v %v", result, err)
		}
	}
}

func TestBrowserUseRejectsAmbiguousReferenceAndConnectionInputs(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"surface_open", nil},
		{"surface_open", map[string]any{"url": "example.com"}},
		{"surface_open", map[string]any{"url": "https://"}},
		{"surface_open", map[string]any{"url": "file:///tmp/report.html"}},
		{"surface_open", map[string]any{"url": "@chat/report.html"}},
		{"surface_open", map[string]any{"url": "https://user:secret@example.com"}},
		{"surface_open", map[string]any{"url": "https://example.com", "surfaceId": "page:unowned"}},
		{"surface_open", map[string]any{"url": "https://example.com", "source": map[string]any{"chatId": "forged"}}},
		{"surface_observe", nil},
		{"surface_observe", map[string]any{"surfaceId": " "}},
		{"surface_observe", map[string]any{"surfaceId": "page:1", "provider": "chrome"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "observationId": "o1", "selector": "#button"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": " "}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": 1, "observationId": "o1"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "selector": "#button"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "x": 1.0, "y": 2.0}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "y": 2.0}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "waitFor": "visible"}},
		{"surface_click", map[string]any{"surfaceId": "page:1", "selector": "#button", "timeoutMs": 1000.0}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "ref": "e1", "action": "focus"}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "selector": "#button", "action": "focus"}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "action": "click"}},
		{"surface_element", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "action": "fill", "value": false}},
	} {
		result, err := executor.Invoke(context.Background(), tc.tool, tc.args, execCtx)
		if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
			t.Errorf("%s %v: result=%#v err=%v", tc.tool, tc.args, result, err)
		}
	}
	for _, timeout := range []any{nil, "1000", true, -1.0, 99.0, 10001.0, 250.5, math.NaN(), math.Inf(1)} {
		result, err := executor.Invoke(context.Background(), "surface_click", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1", "timeoutMs": timeout}, execCtx)
		if err != nil || result.Error != "invalid_args" || result.ExitCode != -1 {
			t.Errorf("timeout=%v: result=%#v err=%v", timeout, result, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("invalid or forged inputs reached Desktop: %#v", invoker.requests)
	}
}

func TestSurfaceObservePreservesSemanticSnapshot(t *testing.T) {
	observation := map[string]any{
		"surfaceId": "page:chrome-1", "provider": "chrome", "observationId": "o1", "url": "https://example.com/", "title": "Form", "text": "Name Submit",
		"elements": []any{map[string]any{"ref": "e1", "role": "textbox", "name": "Name", "value": "Ada", "tag": "input"}, map[string]any{"ref": "e2", "role": "button", "name": "Submit"}},
	}
	executor, execCtx, _ := webControlTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": observation}
	})
	result, err := executor.Invoke(context.Background(), "surface_observe", map[string]any{"surfaceId": "page:chrome-1"}, execCtx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("observe: %#v %v", result, err)
	}
	response, _ := result.Structured["response"].(map[string]any)
	if !reflect.DeepEqual(response["result"], observation) {
		t.Fatalf("snapshot changed: %#v", response["result"])
	}
}

func TestBrowserConnectionRequiredPreservesHostFailure(t *testing.T) {
	for _, frameError := range []bool{false, true} {
		executor, execCtx, _ := webControlTestRuntime(t, func(ClientRequest) map[string]any {
			return map[string]any{"ok": false, "error": map[string]any{"code": "browser_connection_required", "message": "Connect Chrome to this Chat."}}
		})
		if frameError {
			code := 409
			executor.clientRequest = &scriptedClientRequestInvoker{frames: []ClientResponseFrame{{Frame: "error", Type: "browser_connection_required", Code: &code, Msg: "Connect Chrome to this Chat."}}}
		}
		result, err := executor.Invoke(context.Background(), "surface_open", map[string]any{"url": "https://example.com/"}, execCtx)
		if err != nil || result.ExitCode != -1 || result.Error != "browser_connection_required" || !strings.Contains(result.Output, "Connect Chrome to this Chat.") {
			t.Fatalf("frameError=%t result=%#v err=%v", frameError, result, err)
		}
		encoded, _ := json.Marshal(result.Structured)
		if string(encoded) != result.Output {
			t.Fatalf("result payload diverged: %s %s", encoded, result.Output)
		}
	}
}

func TestBrowserUseUsesNativeMountRunAllowlistAndReadOnlyPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, tool, wantError string
		args                  map[string]any
		prepare               func(*RuntimeToolExecutor, *ExecutionContext)
	}{
		{"unmounted open", "surface_open", "connector_not_mounted", map[string]any{"url": "https://example.com/"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) {
			delete(e.Session.NativeConnectorTools, "surface_open")
		}},
		{"unmounted observe", "surface_observe", "connector_not_mounted", map[string]any{"surfaceId": "page:1"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) {
			delete(e.Session.ConnectorDirs, connector.WebControlConnectorID)
		}},
		{"excluded observe", "surface_observe", "tool_not_mounted", map[string]any{"surfaceId": "page:1"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) {
			e.Session.ToolSetFrozen = true
			e.Session.ToolNames = []string{"surface_list"}
		}},
		{"read only observe", "surface_observe", "", map[string]any{"surfaceId": "page:1"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"read only open", "surface_open", "btw_tool_disabled", map[string]any{"url": "https://example.com/"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"read only click", "surface_click", "btw_tool_disabled", map[string]any{"surfaceId": "page:1", "ref": "e1", "observationId": "o1"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"read only scroll", "surface_scroll", "btw_tool_disabled", map[string]any{"surfaceId": "page:1", "deltaY": 100.0}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"read only dialog", "surface_dialog", "btw_tool_disabled", map[string]any{"surfaceId": "page:1", "observationId": "o1", "dialogId": "d1", "action": "accept"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"read only upload", "surface_upload", "btw_tool_disabled", map[string]any{"surfaceId": "page:1", "observationId": "o1", "ref": "e1", "referenceNames": []any{"report.txt"}}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"read only downloads", "surface_downloads", "btw_tool_disabled", map[string]any{"surfaceId": "page:1", "action": "list"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) { e.ToolExecutionPolicy = ToolExecutionPolicyReadOnly }},
		{"unmounted dialog", "surface_dialog", "connector_not_mounted", map[string]any{"surfaceId": "page:1", "observationId": "o1", "dialogId": "d1", "action": "accept"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) {
			delete(e.Session.NativeConnectorTools, "surface_dialog")
		}},
		{"excluded download", "surface_downloads", "tool_not_mounted", map[string]any{"surfaceId": "page:1", "action": "list"}, func(_ *RuntimeToolExecutor, e *ExecutionContext) {
			e.Session.ToolSetFrozen = true
			e.Session.ToolNames = []string{"surface_list"}
		}},
		{"standalone observe", "surface_observe", "desktop_cdp_unsupported_runtime", map[string]any{"surfaceId": "page:1"}, func(r *RuntimeToolExecutor, _ *ExecutionContext) { r.cfg.RuntimeMode = config.RuntimeModeStandalone }},
		{"standalone open", "surface_open", "desktop_cdp_unsupported_runtime", map[string]any{"url": "https://example.com/"}, func(r *RuntimeToolExecutor, _ *ExecutionContext) { r.cfg.RuntimeMode = config.RuntimeModeStandalone }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, execCtx, invoker := webControlTestRuntime(t, nil)
			defs, err := LoadEmbeddedToolDefinitions()
			if err != nil {
				t.Fatal(err)
			}
			for _, def := range defs {
				if owner, ok := connector.NativeToolConnector(def.Name); ok && owner == connector.WebControlConnectorID {
					executor.defs = append(executor.defs, def)
				}
			}
			tc.prepare(executor, execCtx)
			router := mustNewToolRouter(t, executor, nil, nil, nil)
			result, err := router.Invoke(context.Background(), tc.tool, tc.args, execCtx)
			if err != nil || result.Error != tc.wantError {
				t.Fatalf("result=%#v err=%v want=%s", result, err, tc.wantError)
			}
			if tc.wantError != "" && len(invoker.requests) != 0 || tc.wantError == "" && len(invoker.requests) != 1 {
				t.Fatalf("unexpected dispatch count: %d", len(invoker.requests))
			}
		})
	}
}
