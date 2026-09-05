package runstate

import (
	"context"
	"errors"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestManagerOwnsExclusiveRunAndEventBus(t *testing.T) {
	manager := NewManager()
	registration, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{
		RunID:     "run-1",
		RequestID: "request-1",
		ChatID:    "chat-1",
		AgentKey:  "agent-1",
		RunOwner:  contracts.AgentRunOwner("agent-1", ""),
	})
	if err != nil || !registration.Registered {
		t.Fatalf("register = %#v, err=%v", registration, err)
	}
	if _, ok := manager.EventBus("run-1"); !ok {
		t.Fatal("registered run has no event bus")
	}

	duplicate, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{
		RunID:     "run-2",
		RequestID: "request-2",
		ChatID:    "chat-1",
		AgentKey:  "agent-1",
		RunOwner:  contracts.AgentRunOwner("agent-1", ""),
	})
	if err != nil || duplicate.Registered || duplicate.ActiveRun.RunID != "run-1" {
		t.Fatalf("duplicate register = %#v, err=%v", duplicate, err)
	}
}

func TestManagerCoordinatesHistoryCompactAndQueryAdmission(t *testing.T) {
	manager := NewManager()
	release, err := manager.ReserveChatQuery("chat-1", "query-1")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.CompactControlRequest{RequestID: "compact-1", CompactID: "cid-1", ChatID: "chat-1"}
	if ack, err := manager.RouteCompactForChat(request); err != nil || ack.Status != "busy" {
		t.Fatalf("compact during admission = %#v, err=%v", ack, err)
	}
	release()

	owner, err := manager.RouteCompactForChat(request)
	if err != nil || owner.Status != "history_acquired" {
		t.Fatalf("compact owner = %#v, err=%v", owner, err)
	}
	if _, err := manager.ReserveChatQuery("chat-1", "query-2"); err == nil {
		t.Fatal("query admission succeeded during compact")
	} else {
		var conflict *contracts.ChatMaintenanceConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("admission error = %T %v", err, err)
		}
	}

	result := api.CompactResponse{Accepted: true, Status: "completed", RequestID: request.RequestID, CompactID: request.CompactID}
	manager.CompleteChatMaintenance(request.ChatID, request.RequestID, result)
	<-owner.Handle.Done()
	if got := owner.Handle.Result(); got.Status != "completed" || got.CompactID != request.CompactID {
		t.Fatalf("compact result = %#v", got)
	}
}
