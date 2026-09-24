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
	Digest   string
}

// AgentServerKey keeps process/session identity distinct from package identity.
func AgentServerKey(agentKey, serverKey string) string {
	hash := sha256.Sum256([]byte(agentKey + "\x00" + serverKey))
	return fmt.Sprintf("agent-mcp-%x", hash[:16])
}

// AgentVersionServerKey permits old and new package sessions to coexist.
func AgentVersionServerKey(agentKey, serverKey, digest string) string {
	if digest == "" {
		return AgentServerKey(agentKey, serverKey)
	}
	return AgentServerKey(agentKey, serverKey+"@"+digest)
}
