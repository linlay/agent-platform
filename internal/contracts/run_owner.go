package contracts

import "strings"

// RunOwnerType identifies the public principal that owns a run. The execution
// agent is deliberately kept separate so an internal coordinator can execute a
// team-owned run without exposing a synthetic agent key to clients.
type RunOwnerType string

const (
	RunOwnerTypeAgent RunOwnerType = "agent"
	RunOwnerTypeTeam  RunOwnerType = "team"
)

// RunOwner is the immutable identity captured when a run is registered.
//
// AgentKey and TeamID are public identity fields. ExecutionAgentKey is runtime
// only and must not be serialized into public API responses. For legacy teams,
// Type remains agent, AgentKey is the selected member, and TeamID is retained as
// context so existing team admission and control behavior stays compatible.
type RunOwner struct {
	Type              RunOwnerType
	AgentKey          string
	TeamID            string
	ExecutionAgentKey string
}

func AgentRunOwner(agentKey string, teamID string) RunOwner {
	agentKey = strings.TrimSpace(agentKey)
	return RunOwner{
		Type:              RunOwnerTypeAgent,
		AgentKey:          agentKey,
		TeamID:            strings.TrimSpace(teamID),
		ExecutionAgentKey: agentKey,
	}
}

func TeamRunOwner(teamID string, executionAgentKey string) RunOwner {
	return RunOwner{
		Type:              RunOwnerTypeTeam,
		TeamID:            strings.TrimSpace(teamID),
		ExecutionAgentKey: strings.TrimSpace(executionAgentKey),
	}
}

// ResolveRunOwner normalizes an explicitly supplied owner and fills runtime
// fallbacks from the existing QuerySession identity fields. An omitted owner is
// always interpreted as an agent owner, preserving all pre-Team-runtime callers.
func ResolveRunOwner(owner RunOwner, agentKey string, teamID string) RunOwner {
	owner.Type = RunOwnerType(strings.ToLower(strings.TrimSpace(string(owner.Type))))
	owner.AgentKey = strings.TrimSpace(owner.AgentKey)
	owner.TeamID = strings.TrimSpace(owner.TeamID)
	owner.ExecutionAgentKey = strings.TrimSpace(owner.ExecutionAgentKey)
	agentKey = strings.TrimSpace(agentKey)
	teamID = strings.TrimSpace(teamID)

	if owner.Type == RunOwnerTypeTeam {
		owner.AgentKey = ""
		if owner.TeamID == "" {
			owner.TeamID = teamID
		}
		if owner.ExecutionAgentKey == "" {
			owner.ExecutionAgentKey = agentKey
		}
		return owner
	}

	owner.Type = RunOwnerTypeAgent
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
	return ResolveRunOwner(o, "", "").Type == RunOwnerTypeTeam
}

func firstNonBlankRunOwner(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
