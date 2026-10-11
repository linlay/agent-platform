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
func ResolveTeam(registry catalog.Registry, agentKey string) (catalog.TeamSnapshot, bool) {

	if registry == nil || strings.TrimSpace(agentKey) == "" {
		return catalog.TeamSnapshot{}, false
	}
	if resolver, ok := registry.(catalog.TeamResolver); ok {
		return resolver.ResolveTeam(agentKey)
	}

	// Compatibility path for narrow registries used by embedders and tests.
	// Production FileRegistry takes the atomic TeamResolver path above.
	team, ok := registry.AgentDefinition(agentKey)
	if !ok || team.Mode != "TEAM" || team.TeamConfig == nil {
		return catalog.TeamSnapshot{}, false
	}
	agents := make(map[string]catalog.AgentDefinition, len(team.TeamConfig.Members))
	for _, raw := range team.TeamConfig.Members {
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

func AcquireAgentSnapshot(registry catalog.Registry, def catalog.AgentDefinition) (catalog.AgentDefinition, func(), bool) {
	if leases, ok := registry.(interface {
		AcquireAgentSnapshot(catalog.AgentDefinition) (catalog.AgentDefinition, func(), bool)
	}); ok {
		return leases.AcquireAgentSnapshot(def)
	}
	return def, func() {}, true
}
func AcquireTeamSnapshot(registry catalog.Registry, team catalog.TeamSnapshot) (catalog.TeamSnapshot, func(), bool) {
	if leases, ok := registry.(interface {
		AcquireTeamSnapshot(catalog.TeamSnapshot) (catalog.TeamSnapshot, func(), bool)
	}); ok {
		return leases.AcquireTeamSnapshot(team)
	}
	return team, func() {}, true
}

func AcquireRun(registry catalog.Registry, key string) (catalog.AgentDefinition, *catalog.TeamSnapshot, func(), bool) {
	if leaser, ok := registry.(interface {
		AcquireRunRuntime(string) (catalog.AgentDefinition, *catalog.TeamSnapshot, func(), bool)
	}); ok {
		return leaser.AcquireRunRuntime(key)
	}
	def, ok := registry.AgentDefinition(key)
	if !ok {
		return def, nil, nil, false
	}
	if def.Mode == "TEAM" {
		snapshot, release, ok := AcquireTeam(registry, key)
		return snapshot.Coordinator, &snapshot, release, ok
	}
	def, release, ok := AcquireAgent(registry, key)
	return def, nil, release, ok
}
