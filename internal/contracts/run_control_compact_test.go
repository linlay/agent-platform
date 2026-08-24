package contracts

import (
	"context"
	"errors"
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

func TestRunManagerCoordinatesHistoryMaintenanceWithQueryAdmission(t *testing.T) {
	manager := NewInMemoryRunManager()
	release, err := manager.ReserveChatQuery("chat-lease", "query-1")
	if err != nil {
		t.Fatalf("ReserveChatQuery: %v", err)
	}
	request := CompactControlRequest{RequestID: "compact-1", CompactID: "cid-1", ChatID: "chat-lease", Trigger: "manual", Level: "summary"}
	if ack, err := manager.RouteCompactForChat(request); err != nil || ack.Status != "busy" {
		t.Fatalf("compact while query is preparing = %#v err=%v", ack, err)
	}
	release()
	owner, err := manager.RouteCompactForChat(request)
	if err != nil || owner.Status != "history_acquired" {
		t.Fatalf("history owner = %#v err=%v", owner, err)
	}
	joined, err := manager.RouteCompactForChat(request)
	if err != nil || joined.Status != "history_joined" {
		t.Fatalf("history join = %#v err=%v", joined, err)
	}
	if ack, err := manager.RouteCompactForChat(CompactControlRequest{RequestID: "compact-2", CompactID: "cid-2", ChatID: "chat-lease"}); err != nil || ack.Status != "busy" {
		t.Fatalf("different compact while leased = %#v err=%v", ack, err)
	}
	if _, err := manager.ReserveChatQuery("chat-lease", "query-2"); err == nil {
		t.Fatal("query admission succeeded during compact lease")
	} else {
		var conflict *ChatMaintenanceConflictError
		if !errors.As(err, &conflict) || conflict.Detail != "compact_in_progress" {
			t.Fatalf("query conflict = %T %v", err, err)
		}
	}
	if _, err := manager.RegisterExclusiveForChat(context.Background(), QuerySession{RunID: "run-2", RequestID: "query-2", ChatID: "chat-lease"}); err == nil {
		t.Fatal("run registration succeeded during compact lease")
	}
	want := api.CompactResponse{Accepted: true, Status: "completed", RequestID: request.RequestID, CompactID: request.CompactID, ChatID: request.ChatID, Scope: "history"}
	manager.CompleteChatMaintenance(request.ChatID, request.RequestID, want)
	<-owner.Handle.Done()
	<-joined.Handle.Done()
	if owner.Handle.Result().CompactID != want.CompactID || joined.Handle.Result().Status != "completed" {
		t.Fatalf("maintenance results owner=%#v joined=%#v", owner.Handle.Result(), joined.Handle.Result())
	}
	replayed, err := manager.RouteCompactForChat(request)
	if err != nil || replayed.Status != "completed" || replayed.Handle.Result().CompactID != want.CompactID {
		t.Fatalf("maintenance replay = %#v err=%v", replayed, err)
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

func TestRunManagerMarksNativeSessionCompactableAtRegistration(t *testing.T) {
	manager := NewInMemoryRunManager()
	_, nativeControl, _ := manager.Register(context.Background(), QuerySession{
		RunID:                     "run-native-compact",
		ChatID:                    "chat-native-compact",
		SupportsContextCompaction: true,
	})
	defer manager.Finish("run-native-compact")
	if !nativeControl.ContextCompactSupported() {
		t.Fatal("native run was not marked compactable during registration")
	}
	ack, err := manager.RouteCompactForChat(CompactControlRequest{
		RequestID: "request-native-compact",
		CompactID: "compact-native",
		ChatID:    "chat-native-compact",
		Trigger:   "manual",
		Level:     "summary",
	})
	if err != nil || ack.Status != "queued" {
		t.Fatalf("native compact route = %#v err=%v", ack, err)
	}

	manager.Finish("run-native-compact")
	_, unsupportedControl, _ := manager.Register(context.Background(), QuerySession{
		RunID:  "run-proxy-compact",
		ChatID: "chat-proxy-compact",
	})
	defer manager.Finish("run-proxy-compact")
	if unsupportedControl.ContextCompactSupported() {
		t.Fatal("unsupported run was marked compactable")
	}
	ack, err = manager.RouteCompactForChat(CompactControlRequest{
		RequestID: "request-proxy-compact",
		CompactID: "compact-proxy",
		ChatID:    "chat-proxy-compact",
		Trigger:   "manual",
		Level:     "summary",
	})
	if err != nil || ack.Status != "unsupported" {
		t.Fatalf("unsupported compact route = %#v err=%v", ack, err)
	}
}
