package runstate

import (
	"context"
	"testing"

	"agent-platform/internal/contracts"
)

func TestManagerStatusKeepsTeamCoordinatorPrivate(t *testing.T) {
	runs := newTestManager(t)
	_, _, active := runs.Register(context.Background(), contracts.QuerySession{
		RunID:    "run-team-owner",
		ChatID:   "chat-team-owner",
		AgentKey: "research",

		RunOwner: contracts.AgentRunOwner("research"),
	})
	if active.AgentKey != "research" {
		t.Fatalf("unexpected active run %#v", active)
	}
	if active.AgentKey != "research" {
		t.Fatalf("execution agent = %q", active.AgentKey)
	}

	status, ok := runs.RunStatus("run-team-owner")
	if !ok {
		t.Fatal("team run status not found")
	}
	if status.AgentKey != "research" {
		t.Fatalf("unexpected run status %#v", status)
	}
	if status.AgentKey != "research" {
		t.Fatalf("status execution agent = %q", status.AgentKey)
	}
}
