package session

import (
	"agent-platform/internal/catalog"
)

func IsProxyRoutedAgent(def catalog.AgentDefinition) bool {
	backend := catalog.ExecutionBackend(def)
	return backend == "proxy" || backend == "channel"
}
