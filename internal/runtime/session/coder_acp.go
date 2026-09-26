package session

import (
	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/catalog"
)

func IsProxyRoutedAgent(def catalog.AgentDefinition) bool {
	return IsProxyAgentMode(def.Mode) || catalog.AgentIsChannelMode(def.Mode) || agentbuiltin.IsCoderACPBackend(def.Mode, def.ACPBridgeID)
}
