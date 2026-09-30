package contracts

import "strings"

// AllowsTool checks the session's effective tool set, including mode-local
// tools assembled by the trusted session builder. Unbound internal callers
// (no frozen set and nil ToolNames) retain the internal executor API.
func (s QuerySession) AllowsTool(name string) bool {
	if !s.ToolSetFrozen && s.ToolNames == nil {
		return true
	}
	for _, allowed := range s.ToolNames {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func ToolNotMountedResult(name string) ToolExecutionResult {
	return ToolExecutionResult{Error: "tool_not_mounted", Output: "tool is not available in this session: " + strings.TrimSpace(name), ExitCode: -1}
}
