package catalogview

import (
	"strings"

	"agent-platform/internal/catalog"
)

func AcquireAgent(registry catalog.Registry, key string) (catalog.AgentDefinition, func(), bool) {
	if registry == nil {
		return catalog.AgentDefinition{}, nil, false
	}
	if leases, ok := registry.(catalog.RuntimeLeaser); ok {
		return leases.AcquireAgentRuntime(key)
	}
	def, ok := registry.AgentDefinition(key)
	return def, func() {}, ok
}
func AcquireTeam(registry catalog.Registry, key string) (catalog.TeamSnapshot, func(), bool) {
	if leases, ok := registry.(catalog.RuntimeLeaser); ok {
		return leases.AcquireTeamRuntime(key)
	}
	team, ok := ResolveTeam(registry, key)
	return team, func() {}, ok
}
func ResolveTeam(registry catalog.Registry, teamID string) (catalog.TeamSnapshot, bool) {
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
