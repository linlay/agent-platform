package runstate

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestSnapshotAwaitingSummarySurvivesEventEviction(t *testing.T) {
	manager := NewManager()
	manager.eventBusMaxEvents = 1
	_, control, _ := manager.Register(context.Background(), contracts.QuerySession{RunID: "r", ChatID: "c", AccessLevel: "ask_for_approval"})
	defer control.Finish()
	summaries, truncated := contracts.SummarizeApprovals([]any{
		map[string]any{"toolName": "bash", "description": strings.Repeat("删", 201), "policy": map[string]any{"reason": "destructive"}, "command": "secret"},
	})
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "b", Mode: "form", ItemCount: 2})
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "a", Mode: "approval", ItemCount: 1, Summaries: summaries, SummariesTruncated: truncated})
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	control.UpdateAccessLevel("auto_approve")
	bus, _ := manager.EventBus("r")
	bus.Publish(stream.EventData{Type: "awaiting.ask", Payload: map[string]any{"awaitingId": "a", "command": "secret"}})
	bus.Publish(stream.EventData{Type: "other"})
	for range 10 {
		snapshot, err := Snapshot(manager, nil, "r")
		if err != nil || snapshot.AccessLevel != "auto_approve" || snapshot.AwaitingCount != 2 || snapshot.Awaiting == nil {
			t.Fatalf("%#v %v", snapshot, err)
		}
		a := snapshot.Awaiting
		if a.Mode != "approval" || a.AwaitingID != "a" || a.ItemCount != 1 || !a.Truncated || a.Payload != nil || len(a.Summaries) != 1 || len([]rune(a.Summaries[0].Description)) != 200 || a.Summaries[0].Reason != "destructive" {
			t.Fatalf("%#v", a)
		}
	}
	control.TransitionState(contracts.RunLoopStateModelStreaming)
	snapshot, _ := Snapshot(manager, nil, "r")
	if snapshot.Awaiting != nil || snapshot.AwaitingCount != 0 {
		t.Fatalf("stale awaiting: %#v", snapshot)
	}
}

func TestSnapshotAllAwaitingModes(t *testing.T) {
	for _, mode := range []string{"question", "approval", "form", "planning"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager()
			_, control, _ := manager.Register(context.Background(), contracts.QuerySession{RunID: "r", ChatID: "c"})
			defer control.Finish()
			control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "a", Mode: mode, ItemCount: 1, Questions: []any{"q"}})
			control.TransitionState(contracts.RunLoopStateWaitingSubmit)
			snapshot, err := Snapshot(manager, nil, "r")
			if err != nil || snapshot.Awaiting == nil || snapshot.Awaiting.Mode != mode || snapshot.Awaiting.ItemCount != 1 {
				t.Fatalf("%#v %v", snapshot, err)
			}
		})
	}
}
