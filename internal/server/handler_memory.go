package server

import (
	"agent-platform/internal/catalog"
	"strings"
)

func (s *Server) memorySystemEnabled() bool {
	return s != nil && s.deps.Config.Memory.Enabled
}

func (s *Server) memoryEnabledForAgent(agentDef catalog.AgentDefinition) bool {
	return s.memorySystemEnabled() && agentDef.MemoryEnabled
}

func (s *Server) memoryEnabledForAgentKey(agentKey string) bool {
	if !s.memorySystemEnabled() || s == nil || s.deps.Registry == nil {
		return false
	}
	def, ok := s.deps.Registry.AgentDefinition(strings.TrimSpace(agentKey))
	return ok && def.MemoryEnabled
}
