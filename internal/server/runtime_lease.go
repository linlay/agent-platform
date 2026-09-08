package server

import "agent-platform/internal/catalog"

func acquireAgentRuntime(registry catalog.Registry, key string) (catalog.AgentDefinition, func(), bool) {
	if registry == nil {
		return catalog.AgentDefinition{}, nil, false
	}
	if leases, ok := registry.(catalog.RuntimeLeaser); ok {
		return leases.AcquireAgentRuntime(key)
	}
	def, ok := registry.AgentDefinition(key)
	return def, func() {}, ok
}

func acquireTeamRuntime(registry catalog.Registry, key string) (catalog.TeamSnapshot, func(), bool) {
	if leases, ok := registry.(catalog.RuntimeLeaser); ok {
		return leases.AcquireTeamRuntime(key)
	}
	team, ok := resolveCatalogTeam(registry, key)
	return team, func() {}, ok
}
