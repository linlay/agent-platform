package contracts

import "strings"

// RunOwner is the immutable root Agent identity captured at registration.
// A member session has its own AgentKey and retains the root RunOwner.
type RunOwner struct{ AgentKey string }

func AgentRunOwner(agentKey string) RunOwner { return RunOwner{AgentKey: strings.TrimSpace(agentKey)} }
func ResolveRunOwner(owner RunOwner) RunOwner {
	owner.AgentKey = strings.TrimSpace(owner.AgentKey)
	return owner
}
