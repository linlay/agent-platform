package session

import (
	"agent-platform/internal/catalog"
)

func (s *Builder) AgentUsesContainerHub(def catalog.AgentDefinition) bool {
	if s == nil || s.deps.Config.IsLocalMode() {
		return false
	}
	return s.deps.Config.ContainerHub.Enabled && HasRuntimeSandbox(def.Runtime)
}
