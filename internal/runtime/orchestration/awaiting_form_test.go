package orchestration

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	"agent-platform/internal/view"
	"context"
	"testing"
)

func TestTeamFormKeepsValidationSnapshotAtPublicAwaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent := contracts.NewRunControl(ctx, "team")
	defer parent.Finish()
	child := contracts.NewRunControl(ctx, "member")
	defer child.Finish()
	ask := stream.AwaitAsk{AwaitingID: "raw", Mode: "form", View: view.Builtin("ask_user_form"), Form: map[string]any{"title": "Choice", "data": map[string]any{"html": `<input name="choice">`}}}
	item := &TeamChildAwaiting{Ask: ask, RawID: "raw", PublicID: "task/raw", Control: child, Task: PreparedSubTask{TaskID: "task"}}
	queue := &TeamHITLQueue{Orchestrator: &Coordinator{RunCtx: ctx, EmitInputs: func(inputs ...stream.StreamInput) {}}}
	queue.publish(parent, item)
	awaiting, ok := parent.LookupAwaiting("task/raw")
	if !ok || awaiting.View == nil || awaiting.View.Key != "ask_user_form" || awaiting.Form["data"].(map[string]any)["html"] != `<input name="choice">` {
		t.Fatalf("lost form validation context: %#v", awaiting)
	}
	ask.Form["data"].(map[string]any)["html"] = "changed"
	awaiting, _ = parent.LookupAwaiting("task/raw")
	if awaiting.Form["data"].(map[string]any)["html"] != `<input name="choice">` {
		t.Fatal("shared event mutated waiter snapshot")
	}
}
