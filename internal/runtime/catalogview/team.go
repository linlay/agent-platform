package catalogview

import (
	"fmt"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

func ResolveQueryTeam(
	registry catalog.Registry,
	requestedTeamID string,
	requestedAgentKey string,
	existing *chat.Summary,
) (string, string, *catalog.TeamSnapshot, *runtimetypes.RequestError) {
	requestedTeamID = strings.TrimSpace(requestedTeamID)
	requestedAgentKey = strings.TrimSpace(requestedAgentKey)
	if requestedTeamID != "" && requestedAgentKey != "" {
		return "", "", nil, &runtimetypes.RequestError{Status: 400, Code: "invalid_request", Message: "agentKey must be omitted for a Team"}
	}
	existingTeamID := ""
	if existing != nil {
		if strings.TrimSpace(existing.TeamID) != "" && strings.TrimSpace(existing.AgentKey) != "" {
			return "", "", nil, &runtimetypes.RequestError{Status: 400, Code: "invalid_request", Message: "historical Team chat cannot be resumed; create a new Team chat using teamId only"}
		}
		existingTeamID = strings.TrimSpace(existing.TeamID)
		if requestedTeamID != "" && requestedTeamID != existingTeamID {
			return "", "", nil, &runtimetypes.RequestError{
				Status:  409,
				Code:    "team_conflict",
				Message: "teamId does not match chat",
			}
		}
	}

	teamID := requestedTeamID
	if teamID == "" && existing != nil {
		teamID = existingTeamID
	}
	if teamID == "" {
		if requestedAgentKey == "" && existing != nil {
			requestedAgentKey = strings.TrimSpace(existing.AgentKey)
		}
		return "", requestedAgentKey, nil, nil
	}

	snapshot, ok := ResolveTeam(registry, teamID)
	if !ok {
		status := 400
		code := "invalid_request"
		if existing != nil && existingTeamID == teamID {
			status = 503
			code = "unavailable"
		}
		return "", "", nil, &runtimetypes.RequestError{
			Status:  status,
			Code:    code,
			Message: fmt.Sprintf("team %q not found", teamID),
		}
	}
	if requestedAgentKey != "" {
		return "", "", nil, &runtimetypes.RequestError{Status: 400, Code: "invalid_request", Message: "agentKey must be omitted for a Team"}
	}
	if len(snapshot.AgentKeys) == 0 || len(snapshot.InvalidAgentKeys) > 0 || len(snapshot.ValidAgentKeys) != len(snapshot.AgentKeys) {
		return "", "", nil, &runtimetypes.RequestError{Status: 503, Code: "unavailable", Message: fmt.Sprintf("Team %q has unavailable members: %v", teamID, snapshot.InvalidAgentKeys)}
	}
	var unrunnable []string
	for _, memberKey := range snapshot.ValidAgentKeys {
		member, exists := snapshot.AgentDefinition(memberKey)
		if !exists || catalog.AgentUsesACPCoderBackend(member) || !sessionbuild.ResolvedModeCapabilities(member).RunAsChild {
			unrunnable = append(unrunnable, memberKey)
		}
	}
	if len(unrunnable) > 0 {
		return "", "", nil, &runtimetypes.RequestError{Status: 503, Code: "unavailable", Message: fmt.Sprintf("Team %q has members that cannot run as children: %v", teamID, unrunnable)}
	}
	copy := snapshot
	return teamID, "", &copy, nil
}
