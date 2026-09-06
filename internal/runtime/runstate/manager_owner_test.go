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
		AgentKey: "__team_coordinator",
		TeamID:   "team-a",
		RunOwner: contracts.TeamRunOwner("team-a", "__team_coordinator"),
	})
	if !contracts.IsTeamRunOwner(active.AgentKey, active.TeamID) || active.AgentKey != "" || active.TeamID != "team-a" {
		t.Fatalf("unexpected active run %#v", active)
	}
	if active.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("execution agent = %q", active.ExecutionAgentKey)
	}

	status, ok := runs.RunStatus("run-team-owner")
	if !ok {
		t.Fatal("team run status not found")
	}
	if !contracts.IsTeamRunOwner(status.AgentKey, status.TeamID) || status.AgentKey != "" || status.TeamID != "team-a" {
		t.Fatalf("unexpected run status %#v", status)
	}
	if status.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("status execution agent = %q", status.ExecutionAgentKey)
	}
}
