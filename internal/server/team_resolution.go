package server

import (
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
)

func resolveCatalogTeam(registry catalog.Registry, teamID string) (catalog.TeamSnapshot, bool) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" || registry == nil {
		return catalog.TeamSnapshot{}, false
	}
	if resolver, ok := registry.(catalog.TeamResolver); ok {
		return resolver.ResolveTeam(teamID)
	}

	// Compatibility path for narrow registries used by embedders and tests.
	// Production FileRegistry takes the atomic TeamResolver path above.
	team, ok := registry.TeamDefinition(teamID)
	if !ok {
		return catalog.TeamSnapshot{}, false
	}
	agents := make(map[string]catalog.AgentDefinition, len(team.AgentKeys))
	for _, raw := range team.AgentKeys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if def, exists := registry.AgentDefinition(key); exists {
			agents[key] = def
		}
	}
	return catalog.NewTeamSnapshot(team, agents), true
}

func resolveQueryTeam(
	registry catalog.Registry,
	requestedTeamID string,
	requestedAgentKey string,
	existing *chat.Summary,
) (string, string, *catalog.TeamSnapshot, *statusError) {
	requestedTeamID = strings.TrimSpace(requestedTeamID)
	requestedAgentKey = strings.TrimSpace(requestedAgentKey)
	existingTeamID := ""
	if existing != nil {
		existingTeamID = strings.TrimSpace(existing.TeamID)
		if requestedTeamID != "" && requestedTeamID != existingTeamID {
			return "", "", nil, &statusError{
				status:  http.StatusConflict,
				code:    "team_conflict",
				message: "teamId does not match chat",
			}
		}
	}

	teamID := requestedTeamID
	if teamID == "" && existing != nil {
		teamID = existingTeamID
	}
	if teamID == "" {
		return "", requestedAgentKey, nil, nil
	}

	snapshot, ok := resolveCatalogTeam(registry, teamID)
	if !ok {
		status := http.StatusBadRequest
		code := "invalid_request"
		if existing != nil && existingTeamID == teamID {
			status = http.StatusServiceUnavailable
			code = "unavailable"
		}
		return "", "", nil, &statusError{
			status:  status,
			code:    code,
			message: fmt.Sprintf("team %q not found", teamID),
		}
	}
	if !snapshot.DefaultAgentValid {
		return "", "", nil, invalidTeamDefaultError(snapshot)
	}

	agentKey := requestedAgentKey
	fromExisting := false
	if agentKey == "" && existing != nil {
		agentKey = strings.TrimSpace(existing.AgentKey)
		fromExisting = true
	}
	if agentKey == "" {
		agentKey = snapshot.DefaultAgentKey
	}

	if snapshot.HasAgent(agentKey) {
		copy := snapshot
		return teamID, agentKey, &copy, nil
	}
	if fromExisting || snapshot.DeclaresAgent(agentKey) {
		return "", "", nil, &statusError{
			status:  http.StatusServiceUnavailable,
			code:    "unavailable",
			message: fmt.Sprintf("team %q current agent %q is unavailable", teamID, agentKey),
		}
	}
	return "", "", nil, &statusError{
		status:  http.StatusForbidden,
		code:    "forbidden",
		message: fmt.Sprintf("agent %q is not in team %q", agentKey, teamID),
	}
}

func invalidTeamDefaultError(snapshot catalog.TeamSnapshot) *statusError {
	return &statusError{
		status: http.StatusServiceUnavailable,
		code:   "unavailable",
		message: fmt.Sprintf(
			"team %q default agent %q is invalid (%s)",
			snapshot.TeamID,
			snapshot.DefaultAgentKey,
			snapshot.DefaultAgentState,
		),
	}
}
