package platformcontrol

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/conversation"
)

func validateChatPinParams(params map[string]any) error {
	if err := requireFields(params, []string{"pinned"}, []string{"chatId"}); err != nil {
		return err
	}
	if _, ok := params["pinned"].(bool); !ok {
		return fmt.Errorf("params.pinned must be a boolean")
	}
	if value, exists := params["chatId"]; exists {
		id, ok := value.(string)
		if !ok || !chat.ValidChatID(strings.TrimSpace(id)) {
			return fmt.Errorf("params.chatId must be a valid non-empty Chat ID; omit it to use the current Chat")
		}
	}
	return nil
}

func chatPinCallerAllowed(execCtx *contracts.ExecutionContext) bool {
	if execCtx == nil {
		return false
	}
	s := execCtx.Session
	owner := contracts.ResolveRunOwner(s.RunOwner)
	if strings.TrimSpace(s.RunID) == "" || strings.TrimSpace(s.AgentKey) == "" ||
		strings.TrimSpace(s.SubTaskID) != "" || strings.TrimSpace(s.TeamID) != "" ||
		s.TeamRuntime != nil || owner.IsTeam() || owner.AgentKey != s.AgentKey ||
		!slices.Contains(s.ToolNames, ToolName) {
		return false
	}
	// ACP sessions have no Platform ToolNames; proxy/channel sessions do not
	// execute native tools. Keep their explicit mode boundary fail-closed here.
	switch strings.ToUpper(strings.TrimSpace(s.Mode)) {
	case "", "TEAM", "ACP", "PROXY", "CHANNEL":
		return false
	}
	return true
}

func (h *ToolHandler) setChatPinned(params map[string]any, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	if !chatPinCallerAllowed(execCtx) {
		return errorResult("chat_pin_forbidden", "chat pinning requires an ordinary native root Agent run with platform_control")
	}
	id := strings.TrimSpace(stringValue(params, "chatId"))
	if _, explicit := params["chatId"]; !explicit {
		id = strings.TrimSpace(execCtx.Session.ChatID)
		if !chat.ValidChatID(id) {
			return errorResult("chat_context_unavailable", "current run has no valid Chat identity; provide an exact chatId")
		}
	}
	if h.chats == nil {
		return errorResult("chat_pin_unavailable", "chat pin service is not configured")
	}
	result, err := h.chats.SetChatPinned(id, params["pinned"].(bool))
	if err != nil {
		var validation *chat.OrderValidationError
		switch {
		case errors.Is(err, chat.ErrChatNotFound):
			return errorResult("chat_not_found", "chat not found")
		case errors.As(err, &validation):
			return errorResult("chat_pin_invalid_target", validation.Error())
		case errors.Is(err, conversation.ErrNotConfigured), errors.Is(err, conversation.ErrPinningNotSupported):
			return errorResult("chat_pin_unavailable", err.Error())
		default:
			return errorResult("chat_pin_failed", "failed to persist Chat pin state")
		}
	}
	return successResult(map[string]any{"chatId": result.ChatID, "pinned": result.Pinned, "changed": result.Changed})
}
