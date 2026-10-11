package contracts

import (
	"agent-platform/internal/view"
	"testing"
)

func TestAwaitingFormSnapshotIsolation(t *testing.T) {
	ctx := AwaitingSubmitContext{View: view.Builtin("ask_user_form"), Form: map[string]any{"title": "Original", "data": map[string]any{"html": `<input name="n">`, "values": map[string]any{"n": []string{"a"}}}}}
	copy := ctx.Clone()
	ctx.View.Key = "changed"
	ctx.Form["data"].(map[string]any)["html"] = "changed"
	ctx.Form["data"].(map[string]any)["values"].(map[string]any)["n"].([]string)[0] = "changed"
	if copy.View.Key != "ask_user_form" || copy.Form["data"].(map[string]any)["html"] != `<input name="n">` || copy.Form["data"].(map[string]any)["values"].(map[string]any)["n"].([]string)[0] != "a" {
		t.Fatal("form snapshot changed with source")
	}
}
