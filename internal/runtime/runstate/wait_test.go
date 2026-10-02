package runstate

import (
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

func TestRunOutcomeAndOriginSurviveManagerRestart(t *testing.T) {
	root := t.TempDir()
	m := NewManager().WithStateRoot(root)
	_, control, _ := m.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "chat", RunOrigin: &contracts.RunOrigin{AgentKey: "caller", Subject: "owner"}})
	bus, _ := m.EventBus("target")
	bus.Publish(stream.EventData{Seq: 1, Type: "run.complete", Timestamp: time.Now().UnixMilli()})
	control.TransitionState(contracts.RunLoopStateCompleted)
	m.Finish("target")
	restarted := NewManager().WithStateRoot(root)
	snapshot, err := restarted.StoredRunSnapshot("target")
	if err != nil || snapshot.Status != "completed" || snapshot.Origin == nil || snapshot.Origin.Subject != "owner" || snapshot.CompletedAt <= 0 {
		t.Fatalf("%+v %v", snapshot, err)
	}
}
