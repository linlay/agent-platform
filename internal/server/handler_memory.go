package server

import "agent-platform/internal/catalog"

func (s *Server) memorySystemEnabled() bool {
	return s != nil && s.deps.Config.Memory.Enabled
}

func (s *Server) memoryEnabledForAgent(agentDef catalog.AgentDefinition) bool {
	return s.memorySystemEnabled() && agentDef.MemoryEnabled
}
