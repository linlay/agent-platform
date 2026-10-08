package app

import (
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/knowledge"
)

// knowledgeCatalogSource is the app-owned mapping from the broad catalog snapshot
// into the mode-neutral capability specs consumed by the KBASE runtime.
type knowledgeCatalogSource struct {
	registry *catalog.FileRegistry
}

func (s knowledgeCatalogSource) Agents() []knowledge.AgentSpec {
	if s.registry == nil {
		return nil
	}
	keys := s.registry.AdminAgentKeys()
	out := make([]knowledge.AgentSpec, 0, len(keys))
	for _, key := range keys {
		if spec, ok := s.Agent(key); ok {
			out = append(out, spec)
		}
	}
	return out
}

func (s knowledgeCatalogSource) Agent(key string) (knowledge.AgentSpec, bool) {
	if s.registry == nil {
		return knowledge.AgentSpec{}, false
	}
	definition, ok := s.registry.AgentDefinition(strings.TrimSpace(key))
	if !ok || !definition.KBaseConfig.Enabled {
		return knowledge.AgentSpec{}, false
	}
	return knowledge.AgentSpec{
		Key:           definition.Key,
		Requirement:   definition.KBaseRequirement,
		WorkspaceRoot: definition.Workspace.Root,
		Config:        definition.KBaseConfig,
	}, true
}
