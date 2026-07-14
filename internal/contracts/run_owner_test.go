package contracts

import (
	"context"
	"testing"
)

func TestResolveRunOwnerPreservesLegacyAgentOwnedTeam(t *testing.T) {
	owner := ResolveRunOwner(RunOwner{}, "member-a", "team-a")
	if owner.IsTeam() {
		t.Fatalf("legacy team owner should remain agent-owned: %#v", owner)
	}
	if owner.AgentKey != "member-a" || owner.TeamID != "team-a" || owner.ExecutionAgentKey != "member-a" {
		t.Fatalf("unexpected legacy owner %#v", owner)
	}
}

func TestResolveRunOwnerSeparatesTeamOwnerFromExecutionAgent(t *testing.T) {
	owner := ResolveRunOwner(TeamRunOwner("team-a", ""), "__team_coordinator", "team-a")
	if !owner.IsTeam() {
		t.Fatalf("orchestrated team owner was not derived from identity: %#v", owner)
	}
	if owner.AgentKey != "" || owner.TeamID != "team-a" || owner.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("unexpected team owner %#v", owner)
	}
}

func TestRunManagerStatusKeepsTeamCoordinatorPrivate(t *testing.T) {
	runs := NewInMemoryRunManager()
	_, _, active := runs.Register(context.Background(), QuerySession{
		RunID:    "run-team-owner",
		ChatID:   "chat-team-owner",
		AgentKey: "__team_coordinator",
		TeamID:   "team-a",
		RunOwner: TeamRunOwner("team-a", "__team_coordinator"),
	})
	if !IsTeamRunOwner(active.AgentKey, active.TeamID) || active.AgentKey != "" || active.TeamID != "team-a" {
		t.Fatalf("unexpected active run %#v", active)
	}
	if active.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("execution agent = %q", active.ExecutionAgentKey)
	}

	status, ok := runs.RunStatus("run-team-owner")
	if !ok {
		t.Fatal("team run status not found")
	}
	if !IsTeamRunOwner(status.AgentKey, status.TeamID) || status.AgentKey != "" || status.TeamID != "team-a" {
		t.Fatalf("unexpected run status %#v", status)
	}
	if status.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("status execution agent = %q", status.ExecutionAgentKey)
	}
}
