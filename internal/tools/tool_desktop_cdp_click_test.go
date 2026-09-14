package tools

import (
	"context"
	"testing"
)

func TestDesktopCDPInputClickForwardsOneRequest(t *testing.T) {
	for _, input := range []map[string]any{
		{"targetId": "target-1", "x": 260.5, "y": 344.0},
		{"targetId": "target-1", "selector": "#button"},
		{"targetId": "target-1", "x": 260.5, "y": 344.0, "waitFor": map[string]any{"selector": "#dialog", "state": "visible"}},
		{"targetId": "target-1", "selector": "#button", "waitFor": map[string]any{"selector": "#check", "state": "checked", "checked": false}},
	} {
		executor, execCtx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
		paramsInput := make(map[string]any)
		for key, value := range input {
			if key != "targetId" {
				paramsInput[key] = value
			}
		}
		result, err := executor.Invoke(context.Background(), "desktop_cdp", map[string]any{"method": "Input.click", "targetId": input["targetId"], "params": paramsInput}, execCtx)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		_, requests := invoker.snapshots()
		if len(requests) != 1 || requests[0].Type != desktopCDPRequestType || requests[0].Payload["method"] != "Input.click" {
			t.Fatalf("requests=%#v", requests)
		}
		params := requests[0].Payload["params"].(map[string]any)
		if _, present := input["waitFor"]; !present {
			if _, present := params["waitFor"]; present {
				t.Fatal("invented waitFor")
			}
		}
		if x, present := params["x"]; present && x != 260.5 {
			t.Fatalf("lost number type: %#v", x)
		}
		source := requests[0].Payload["source"].(map[string]any)
		if source["runId"] != execCtx.Session.RunID {
			t.Fatal("trusted source lost")
		}
	}
}
