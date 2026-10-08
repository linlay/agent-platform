package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/connector"
)

func bindingNames(bindings []api.AgentToolBinding) []string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.Name)
	}
	return names
}

func (s *Server) independentAgentToolNames(names []string) []string {
	mcpNames := map[string]bool{}
	for _, tool := range s.toolSummaries(true) {
		if tool.SourceCategory == "mcp" || tool.SourceType == "mcp" {
			mcpNames[tool.Name] = true
			mcpNames[tool.Key] = true
		}
	}
	result := []string{}
	for _, name := range names {
		_, native := connector.NativeToolConnector(name)
		// Keep unknown declarations for diagnosis; ownership comes from the registry.
		if !native && !mcpNames[name] {
			result = append(result, name)
		}
	}
	return result
}

// Bindings are a management projection; do not mutate the runtime definition.
func (s *Server) independentAgentToolBindings(bindings []api.AgentToolBinding) []api.AgentToolBinding {
	allowed := map[string]bool{}
	for _, name := range s.independentAgentToolNames(bindingNames(bindings)) {
		allowed[name] = true
	}
	result := []api.AgentToolBinding{}
	for _, binding := range bindings {
		if allowed[binding.Name] {
			result = append(result, binding)
		}
	}
	return result
}
