package contracts

import "testing"

func TestRunOwnerIsOnlyRootAgent(t *testing.T) {
	owner := ResolveRunOwner(AgentRunOwner(" research "))
	if owner.AgentKey != "research" {
		t.Fatalf("owner=%#v", owner)
	}
	member := QuerySession{AgentKey: "writer", RunOwner: owner}
	if member.RunOwner.AgentKey != "research" || member.AgentKey != "writer" {
		t.Fatal("member identity changed owner")
	}
	if ResolveRunOwner(RunOwner{}).AgentKey != "" {
		t.Fatal("empty owner populated")
	}
}
