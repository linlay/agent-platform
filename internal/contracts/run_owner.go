package contracts

import "strings"

// RunOwner is the immutable identity captured when a run is registered.
//
// AgentKey and TeamID are public identity fields. ExecutionAgentKey is runtime
// only and must not be serialized into public API responses. An orchestrated
// Team is represented by an empty AgentKey with a non-empty TeamID. Legacy teams
// retain their selected AgentKey alongside TeamID.
type RunOwner struct {
	AgentKey          string
	TeamID            string
	ExecutionAgentKey string
}

func AgentRunOwner(agentKey string, teamID string) RunOwner {
	agentKey = strings.TrimSpace(agentKey)
	return RunOwner{
		AgentKey:          agentKey,
		TeamID:            strings.TrimSpace(teamID),
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

// ResolveRunOwner normalizes an explicitly supplied owner and fills runtime
// fallbacks from the existing QuerySession identity fields.
func ResolveRunOwner(owner RunOwner, agentKey string, teamID string) RunOwner {
	owner.AgentKey = strings.TrimSpace(owner.AgentKey)
	owner.TeamID = strings.TrimSpace(owner.TeamID)
	owner.ExecutionAgentKey = strings.TrimSpace(owner.ExecutionAgentKey)
	agentKey = strings.TrimSpace(agentKey)
	teamID = strings.TrimSpace(teamID)

	if IsTeamRunOwner(owner.AgentKey, owner.TeamID) {
		owner.AgentKey = ""
		if owner.ExecutionAgentKey == "" {
			owner.ExecutionAgentKey = agentKey
		}
		return owner
	}

	if owner.AgentKey == "" {
		owner.AgentKey = agentKey
	}
	if owner.TeamID == "" {
		owner.TeamID = teamID
	}
	if owner.ExecutionAgentKey == "" {
		owner.ExecutionAgentKey = firstNonBlankRunOwner(owner.AgentKey, agentKey)
	}
	return owner
}

func (o RunOwner) IsTeam() bool {
	return IsTeamRunOwner(o.AgentKey, o.TeamID)
}

func firstNonBlankRunOwner(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
