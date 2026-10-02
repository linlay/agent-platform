package tools

import (
	"context"
	"testing"
)

func TestSurfaceClickForwardsOneInputClickRequest(t *testing.T) {
	for _, input := range []map[string]any{
		{"surfaceId": "target-1", "x": 260.5, "y": 344.0},
		{"surfaceId": "target-1", "selector": "#button"},
		{"surfaceId": "target-1", "x": 260.5, "y": 344.0, "waitFor": map[string]any{"selector": "#dialog", "state": "visible"}},
		{"surfaceId": "target-1", "selector": "#button", "waitFor": map[string]any{"selector": "#check", "state": "checked", "checked": false}},
	} {
		executor, execCtx, invoker := desktopCDPParamsTestRuntime(t.TempDir())
		result, err := executor.Invoke(context.Background(), "surface_click", input, execCtx)
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
		if requests[0].Payload["surfaceId"] != "target-1" {
			t.Fatalf("surface lost: %#v", requests[0].Payload)
		}
		if _, leaked := params["surfaceId"]; leaked {
			t.Fatal("surfaceId leaked into params")
		}
		source := requests[0].Payload["source"].(map[string]any)
		if source["runId"] != execCtx.Session.RunID {
			t.Fatal("trusted source lost")
		}
	}
}
