package runstate

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
	"context"
	"testing"
	"time"
)

func TestExplicitRunLifetimeCancelsDuringWaiting(t *testing.T) {
	m := newTestManager(t)
	_, control, _ := m.Register(context.Background(), contracts.QuerySession{RunID: "expired", StartedAtMillis: time.Now().Add(-2 * time.Second).UnixMilli(), ResolvedBudget: contracts.Budget{LifetimeTimeout: 1}})
	defer m.Finish("expired")
	select {
	case <-control.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("lifetime did not cancel Run")
	}
}

func TestReleasedRunSnapshotComesFromChatRecordAndRememberedOrigin(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	startedAt := time.Now().UnixMilli()
	if _, _, err := store.EnsureChat("chat", "agent", "hello"); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"done", "cut-off"} {
		if err := store.OnRunStarted(chat.RunStart{ChatID: "chat", RunID: runID, AgentKey: "agent", StartedAtMillis: startedAt}); err != nil {
			t.Fatal(err)
		}
	}
	writer := chat.NewStepWriter(store, "chat", "done", "GENERAL")
	writer.SetPendingQueryMessages([]map[string]any{{"role": "user", "content": "hello", "ts": startedAt}})
	writer.OnEvent(stream.NewEvent("request.query", map[string]any{"role": "user", "message": "hello", "runId": "done", "chatId": "chat", "accessLevel": "auto_approve"}).Data())
	if err := store.OnRunCompleted(chat.RunCompletion{ChatID: "chat", RunID: "done", AgentKey: "agent", FinishReason: "complete", StartedAtMillis: startedAt, UpdatedAtMillis: startedAt + 1}); err != nil {
		t.Fatal(err)
	}

	// The manager released both Runs; it only remembers who started "done".
	m := NewManager()
	m.runOrigins = map[string]contracts.RunOrigin{"done": {AgentKey: "caller", Subject: "owner"}}
	snapshot, err := Snapshot(m, store, "done")
	if err != nil || snapshot.Status != "completed" || snapshot.ChatID != "chat" || snapshot.AccessLevel != "auto_approve" || snapshot.Origin == nil || snapshot.Origin.Subject != "owner" || snapshot.CompletedAt != startedAt+1 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	if snapshot, err = Snapshot(NewManager(), store, "cut-off"); err != nil || snapshot.Status != "interrupted" || snapshot.CompletedAt <= 0 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	if _, err = Snapshot(NewManager(), store, "missing"); err == nil {
		t.Fatal("unknown Run reported")
	}
}
