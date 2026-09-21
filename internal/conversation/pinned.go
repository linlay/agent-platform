package conversation

import (
	"errors"
	"strings"

	"agent-platform/internal/chat"
)

var ErrPinningNotSupported = errors.New("chat pinning is not supported")

type PinResult struct {
	ChatID  string `json:"chatId"`
	Pinned  bool   `json:"pinned"`
	Changed bool   `json:"changed"`
}

// SetChatPinned is shared by transport and tool callers. Publish immediately
// after persistence, even if a caller's subsequent snapshot read fails.
func (s *Service) SetChatPinned(chatID string, pinned bool) (PinResult, error) {
	result := PinResult{ChatID: strings.TrimSpace(chatID), Pinned: pinned}
	if s == nil || s.Chats == nil {
		return result, ErrNotConfigured
	}
	store, ok := s.Chats.(chat.PinnedStore)
	if !ok {
		return result, ErrPinningNotSupported
	}
	state, changed, err := store.SetChatPinned(result.ChatID, pinned)
	if err != nil {
		return result, err
	}
	result.Changed = changed
	if changed && s.Notifications != nil {
		s.Notifications.Broadcast("chats.order.changed", map[string]any{"updatedAt": state.UpdatedAt})
	}
	return result, nil
}
