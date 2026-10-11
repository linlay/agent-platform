package contracts

import "strings"

// OrdinaryNativeRoot identifies the supported Platform catalog/Chat caller.
// Proxy and ACP execution never create this native tool session.
func OrdinaryNativeRoot(s QuerySession) bool {
	owner := ResolveRunOwner(s.RunOwner)
	if s.RunID == "" || s.AgentKey == "" || s.ChatID == "" || s.SubTaskID != "" || owner.AgentKey != s.AgentKey || s.RunScopeID != "" && s.RunScopeID != s.ChatID {
		return false
	}
	switch strings.ToUpper(s.Mode) {
	case "GENERAL", "CODER", "KBASE", "TEAM":
		return true
	}
	return false
}
