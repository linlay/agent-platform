package mcp

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// A short instance-specific identifier keeps independent Agent snapshots from
// overwriting one another in the global tool router. The wire name is retained
// separately, and is the only name sent to the remote MCP server.
func agentToolName(serverKey, wireName string) string {
	hash := sha256.Sum256([]byte(serverKey + "\x00" + wireName))
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, wireName)
	if len(name) > 43 {
		name = name[:43]
	}
	return fmt.Sprintf("mcp_%x_%s", hash[:8], name)
}
