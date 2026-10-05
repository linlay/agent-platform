package app

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/kbase"
)

// kbaseCatalogSource is the app-owned mapping from the broad catalog snapshot
// into the mode-neutral capability specs consumed by the KBASE runtime.
type kbaseCatalogSource struct {
	registry *catalog.FileRegistry
}

func (s kbaseCatalogSource) Agents() []kbase.AgentSpec {
	if s.registry == nil {
		return nil
	}
	keys := s.registry.AdminAgentKeys()
	out := make([]kbase.AgentSpec, 0, len(keys))
	for _, key := range keys {
		if spec, ok := s.Agent(key); ok {
			out = append(out, spec)
		}
	}
	return out
}

func (s kbaseCatalogSource) Agent(key string) (kbase.AgentSpec, bool) {
	if s.registry == nil {
		return kbase.AgentSpec{}, false
	}
	definition, ok := s.registry.AgentDefinition(strings.TrimSpace(key))
	if !ok || !definition.KBaseConfig.Enabled {
		return kbase.AgentSpec{}, false
	}
	return kbase.AgentSpec{
		Key:           definition.Key,
		Requirement:   definition.KBaseRequirement,
		WorkspaceRoot: definition.Workspace.Root,
		Config:        definition.KBaseConfig,
	}, true
}
