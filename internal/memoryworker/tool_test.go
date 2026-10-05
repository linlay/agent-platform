package memoryworker

import (
	"agent-platform/internal/contracts"
	"context"
	"testing"
)

func TestMemoryUpdateToolRequiresMountedOrdinaryWritableRoot(t *testing.T) {
	cli := &fakeCLI{receipts: map[string]bool{}}
	w, _, _ := workerFixture(t, cli)
	// Mark the queue as available without starting background work in this test.
	w.started = true
	w.ctx = context.Background()
	h := ToolHandler{Worker: w}
	base := contracts.QuerySession{RunID: "r", ChatID: "c", AgentKey: "a", RunOwner: contracts.AgentRunOwner("a", ""), Mode: "GENERAL", AgentHasMemoryConfig: true, ToolNames: []string{"memory_update"}}
	for _, change := range []func(*contracts.QuerySession){func(s *contracts.QuerySession) { s.AgentHasMemoryConfig = false }, func(s *contracts.QuerySession) { s.ToolNames = nil }, func(s *contracts.QuerySession) { s.SubTaskID = "child" }, func(s *contracts.QuerySession) { s.TeamID = "team" }, func(s *contracts.QuerySession) { s.RunOrigin = &contracts.RunOrigin{} }, func(s *contracts.QuerySession) { s.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly }} {
		s := base
		change(&s)
		r, err := h.Invoke(context.Background(), "memory_update", nil, &contracts.ExecutionContext{Session: s})
		if err != nil || r.Error == "" {
			t.Fatal(r, err)
		}
	}
	r, err := h.Invoke(context.Background(), "memory_update", nil, &contracts.ExecutionContext{Session: base})
	if err != nil || r.Error != "" || r.Structured["accepted"] != true {
		t.Fatal(r, err)
	}
	if w.Status().State != "queued" {
		t.Fatal(w.Status())
	}
	if _, err = w.Trigger(); err != nil || len(w.wake) != 1 {
		t.Fatal("trigger did not coalesce", err)
	}
}
