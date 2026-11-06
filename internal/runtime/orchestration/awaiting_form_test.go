package orchestration

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
	"agent-platform/internal/view"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
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

func TestTeamKeepsProducerValidationWhenRelaying(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parent := contracts.NewRunControl(ctx, "team")
	defer parent.Finish()
	ctx = contracts.WithRunControl(ctx, parent)
	task := PreparedSubTask{TaskID: "member"}
	queue := NewTeamHITLQueue(&Coordinator{RunCtx: ctx}, []PreparedSubTask{task}, true)
	child := queue.ControlFor(task)
	defer child.Finish()
	child.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "raw", Mode: "approval", ValidateSubmit: func(req queryinput.SubmitRequest) error {
		if req.OwnerID != "owner" {
			return fmt.Errorf("awaiting_forbidden")
		}
		return nil
	}})
	event, ok := queue.Capture(task, stream.AwaitAsk{AwaitingID: "raw", Mode: "approval", Approvals: []any{map[string]any{"id": "item"}}}, nil)
	if !ok {
		t.Fatal("not captured")
	}
	queue.Observe(event)
	request := queryinput.SubmitRequest{RunID: "team", ChatID: "chat", AwaitingID: event.Awaiting.PublicID, SubmitID: "decision", OwnerID: "other", Params: []json.RawMessage{json.RawMessage(`{"id":"item","decision":"approve"}`)}}
	if ack := parent.ResolveSubmit(request); ack.Status != "awaiting_forbidden" {
		t.Fatalf("lost owner at parent: %+v", ack)
	}
	request.OwnerID = "owner"
	if ack := parent.ResolveSubmit(request); !ack.Accepted {
		t.Fatalf("parent submit=%+v", ack)
	}
	result, err := child.AwaitSubmitWithTimeout(ctx, "raw", time.Second)
	if err != nil || len(result.Request.Params) != 1 {
		t.Fatalf("relay=%+v %v", result, err)
	}
}
