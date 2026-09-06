package contracts

import (
	"context"
	"testing"

	"agent-platform/internal/api"
)

func TestRunControlCompactQueueIsBlockingIdempotentAndExclusive(t *testing.T) {
	control := NewRunControl(context.Background(), "run-compact")
	control.EnableContextCompact()
	request := CompactControlRequest{RequestID: "req-1", CompactID: "compact-1", ChatID: "chat-1", Trigger: "manual", Level: "summary"}
	first, status := control.EnqueueCompact(request)
	if status != "queued" {
		t.Fatalf("first status = %q", status)
	}
	joined, status := control.EnqueueCompact(request)
	if status != "joined" {
		t.Fatalf("joined status = %q", status)
	}
	if _, status := control.EnqueueCompact(CompactControlRequest{RequestID: "req-2", CompactID: "compact-2"}); status != "busy" {
		t.Fatalf("parallel status = %q", status)
	}
	claimed, ok := control.ClaimCompact()
	if !ok || claimed.RequestID != request.RequestID {
		t.Fatalf("claimed = %#v ok=%v", claimed, ok)
	}
	want := api.CompactResponse{Accepted: true, Status: "completed", RequestID: request.RequestID, CompactID: request.CompactID, RunID: "run-compact"}
	if !control.CompleteCompact(request.RequestID, want) {
		t.Fatal("CompleteCompact returned false")
	}
	<-first.Done()
	<-joined.Done()
	if first.Result().CompactID != want.CompactID || joined.Result().Status != "completed" {
		t.Fatalf("results first=%#v joined=%#v", first.Result(), joined.Result())
	}
	replayed, status := control.EnqueueCompact(request)
	if status != "completed" {
		t.Fatalf("completed replay status = %q", status)
	}
	<-replayed.Done()
	if replayed.Result().CompactID != want.CompactID {
		t.Fatalf("replayed result = %#v", replayed.Result())
	}
}

func TestRunControlInterruptResolvesPendingCompact(t *testing.T) {
	control := NewRunControl(context.Background(), "run-compact-interrupt")
	handle, status := control.EnqueueCompact(CompactControlRequest{RequestID: "req", CompactID: "compact", ChatID: "chat"})
	if status != "queued" {
		t.Fatalf("status = %q", status)
	}
	if !control.Interrupt(InterruptInfo{Reason: InterruptReasonUserCancelled}) {
		t.Fatal("interrupt rejected")
	}
	<-handle.Done()
	if got := handle.Result(); got.Status != "failed" || got.Detail != "run_interrupted" || !got.Retryable {
		t.Fatalf("interrupt compact result = %#v", got)
	}
}
