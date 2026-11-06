package contracts

import "strings"

// PrimaryChatTools returns a fresh list so callers cannot alter the policy.
func PrimaryChatTools() []string {
	return []string{"chat_send", "chat_submit", "chat_get_status", "chat_interrupt", "chat_query"}
}

func (s QuerySession) ProvidesPrimaryTool(name string) bool {
	if !s.PrimaryChat || !strings.EqualFold(s.Mode, "GENERAL") || s.SubTaskID != "" {
		return false
	}
	for _, tool := range PrimaryChatTools() {
		if tool == name {
			return true
		}
	}
	return false
}

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
