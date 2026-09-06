package contracts

import (
	"testing"
)

func TestAgentRunOwnerDoesNotRepresentTeamMembership(t *testing.T) {
	owner := ResolveRunOwner(AgentRunOwner(" member-a ", " team-a "))
	if owner.IsTeam() {
		t.Fatalf("agent owner must not become a Team owner: %#v", owner)
	}
	if owner.AgentKey != "member-a" || owner.TeamID != "" || owner.ExecutionAgentKey != "member-a" {
		t.Fatalf("unexpected agent owner %#v", owner)
	}
}

func TestResolveRunOwnerSeparatesTeamOwnerFromExecutionAgent(t *testing.T) {
	owner := ResolveRunOwner(TeamRunOwner(" team-a ", " __team_coordinator "))
	if !owner.IsTeam() {
		t.Fatalf("orchestrated team owner was not derived from identity: %#v", owner)
	}
	if owner.AgentKey != "" || owner.TeamID != "team-a" || owner.ExecutionAgentKey != "__team_coordinator" {
		t.Fatalf("unexpected team owner %#v", owner)
	}
}

func TestResolveRunOwnerDoesNotInferLegacySessionIdentity(t *testing.T) {
	owner := ResolveRunOwner(RunOwner{})
	if owner.AgentKey != "" || owner.TeamID != "" || owner.ExecutionAgentKey != "" {
		t.Fatalf("empty owner was unexpectedly populated: %#v", owner)
	}
}
