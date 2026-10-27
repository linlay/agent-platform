package query

import (
	"errors"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

const primaryChatBusyCode = "primary_chat_busy"

// checkPrimaryChatQuery admits primary:true only for a query that creates the
// Chat (or fills an upload shell) and only while the current primary is idle.
// An existing Chat becomes primary through the explicit primary Chat API.
func (s *Service) checkPrimaryChatQuery(req runtimetypes.QueryCommand, existing *chat.Summary, agentKey string) error {
	if strings.TrimSpace(req.Lane) != "" {
		return &statusError{Status: 400, Code: "invalid_request", Message: "primary is only supported on the main lane"}
	}
	if existing != nil && !chat.IsPendingUploadChat(*existing) {
		return &statusError{Status: 400, Code: "invalid_request", Message: "primary only applies to a query that creates the chat; set an existing chat as primary through /api/chats/primary"}
	}
	if s.deps.PrimaryChats == nil {
		return &statusError{Status: 501, Code: "not_supported", Message: "primary chat is not supported"}
	}
	err := s.deps.PrimaryChats.EnsurePrimaryReplaceable(agentKey)
	var busy *chat.PrimaryChatBusyError
	if errors.As(err, &busy) {
		return &statusError{
			Status:  409,
			Code:    primaryChatBusyCode,
			Message: busy.Error(),
			Data: map[string]any{"error": &queryinput.ChatErrorInfo{
				Code: primaryChatBusyCode, Message: busy.Error(), ChatID: busy.ChatID, RunIDs: busy.RunIDs,
			}},
		}
	}
	return err
}
