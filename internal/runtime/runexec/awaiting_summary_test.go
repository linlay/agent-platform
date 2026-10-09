package runexec

import (
	"context"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestAwaitingLifecycleRetainsBoundedSummary(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run")
	defer control.Finish()
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "merged", Mode: "form", Summaries: []contracts.ApprovalSummary{{ToolName: "bash", Reason: "destructive"}}})
	params := NativeOptions{RunControl: control, Session: contracts.QuerySession{RunID: "run", ChatID: "chat"}}
	tracker := &AwaitingTracker{}
	HandleAwaitingLifecycle(params, stream.EventData{Type: "awaiting.ask", Payload: map[string]any{"awaitingId": "merged", "mode": "form", "form": map[string]any{"title": "review"}}}, tracker)
	merged, ok := control.LookupAwaiting("merged")
	if !ok || len(merged.Summaries) != 1 || merged.ItemCount != 1 {
		t.Fatalf("lost merged metadata: %#v", merged)
	}
	HandleAwaitingLifecycle(params, stream.EventData{Type: "awaiting.ask", Payload: map[string]any{"awaitingId": "approval", "mode": "approval", "approvals": []any{map[string]any{"toolName": "bash", "description": "delete", "policy": map[string]any{"reason": "destructive"}, "command": "secret"}}}}, tracker)
	approval, ok := control.LookupAwaiting("approval")
	if !ok || approval.ItemCount != 1 || len(approval.Summaries) != 1 || approval.Summaries[0].Reason != "destructive" {
		t.Fatalf("%#v", approval)
	}
}
