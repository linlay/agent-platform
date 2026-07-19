package contracts

import "strings"

// RunOwner is the immutable identity captured when a run is registered.
//
// AgentKey and TeamID are public identity fields. ExecutionAgentKey is runtime
// only and must not be serialized into public API responses. An orchestrated
// Team is represented by an empty AgentKey with a non-empty TeamID.
type RunOwner struct {
	AgentKey          string
	TeamID            string
	ExecutionAgentKey string
}

func AgentRunOwner(agentKey string, _ string) RunOwner {
	agentKey = strings.TrimSpace(agentKey)
	return RunOwner{
		AgentKey:          agentKey,
		ExecutionAgentKey: agentKey,
	}
}

func TeamRunOwner(teamID string, executionAgentKey string) RunOwner {
	return RunOwner{
		TeamID:            strings.TrimSpace(teamID),
		ExecutionAgentKey: strings.TrimSpace(executionAgentKey),
	}
}

// IsTeamRunOwner derives the public owner from the two public identity fields.
func IsTeamRunOwner(agentKey string, teamID string) bool {
	return strings.TrimSpace(agentKey) == "" && strings.TrimSpace(teamID) != ""
}

// ResolveRunOwner normalizes an explicitly supplied owner. QuerySession
// producers must set RunOwner instead of relying on AgentKey or TeamID as a
// fallback identity.
func ResolveRunOwner(owner RunOwner) RunOwner {
	owner.AgentKey = strings.TrimSpace(owner.AgentKey)
	owner.TeamID = strings.TrimSpace(owner.TeamID)
	owner.ExecutionAgentKey = strings.TrimSpace(owner.ExecutionAgentKey)

	if IsTeamRunOwner(owner.AgentKey, owner.TeamID) {
		owner.AgentKey = ""
	}
	return owner
}

func (o RunOwner) IsTeam() bool {
	return IsTeamRunOwner(o.AgentKey, o.TeamID)
}
