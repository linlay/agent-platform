package session

import (
	"agent-platform/internal/catalog"
)

func IsProxyAgentMode(mode string) bool {
	return catalog.AgentIsProxyMode(mode)
}
