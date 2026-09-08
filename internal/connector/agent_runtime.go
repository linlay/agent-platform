package connector

import (
	"crypto/sha256"
	"fmt"
)

// AgentRuntime describes a mounted package; it never contains credentials.
type AgentRuntime struct {
	AgentKey string
	ID       string
	Dir      string
}

// AgentServerKey keeps process/session identity distinct from package identity.
func AgentServerKey(agentKey, serverKey string) string {
	hash := sha256.Sum256([]byte(agentKey + "\x00" + serverKey))
	return fmt.Sprintf("agent-mcp-%x", hash[:16])
}
