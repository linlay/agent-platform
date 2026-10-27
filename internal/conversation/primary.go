package conversation

import (
	"errors"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

var ErrPrimaryNotSupported = errors.New("primary chat is not supported")

const PrimaryChangedEvent = "chat.primary.changed"

type PrimaryResult struct {
	AgentKey       string `json:"agentKey"`
	ChatID         string `json:"chatId"`
	PreviousChatID string `json:"previousChatId,omitempty"`
	Changed        bool   `json:"changed"`
}

func (s *Service) primaryStore() (chat.PrimaryStore, error) {
	if s == nil || s.Chats == nil {
		return nil, ErrNotConfigured
	}
	store, ok := s.Chats.(chat.PrimaryStore)
	if !ok {
		return nil, ErrPrimaryNotSupported
	}
	return store, nil
}

// SetChatPrimary makes an existing, published Chat the primary Chat of its
// owning Agent. A new Chat becomes primary through its first query instead.
func (s *Service) SetChatPrimary(chatID string) (PrimaryResult, error) {
	release := s.LockMutation()
	defer release()
	chatID = strings.TrimSpace(chatID)
	result := PrimaryResult{ChatID: chatID}
	store, err := s.primaryStore()
	if err != nil {
		return result, err
	}
	if !chat.ValidChatID(chatID) {
		return result, &chat.OrderValidationError{Message: "invalid chatId"}
	}
	summary, err := s.Chats.Summary(chatID)
	if err != nil {
		return result, err
	}
	if summary == nil {
		return result, chat.ErrChatNotFound
	}
	result.AgentKey = summary.AgentKey
	if strings.TrimSpace(summary.AgentKey) == "" {
		return result, &chat.OrderValidationError{Message: "chat has no owning agent"}
	}
	if chat.IsPendingUploadChat(*summary) {
		return result, &chat.OrderValidationError{Message: "pending upload chat cannot become primary"}
	}
	current, err := store.PrimaryChatID(summary.AgentKey)
	if err != nil {
		return result, err
	}
	if current == chatID {
		result.PreviousChatID = current
		return result, nil
	}
	if err := s.ensureChatIdle(summary.AgentKey, current); err != nil {
		return result, err
	}
	return s.publishPrimary(store, summary.AgentKey, chatID)
}

// EnsurePrimaryReplaceable rejects a switch while the Agent's current primary
// Chat still has a Run, including one waiting for the user.
func (s *Service) EnsurePrimaryReplaceable(agentKey string) error {
	store, err := s.primaryStore()
	if err != nil {
		return err
	}
	current, err := store.PrimaryChatID(agentKey)
	if err != nil {
		return err
	}
	return s.ensureChatIdle(agentKey, current)
}

// PublishPrimaryChat switches the pointer to a Chat the caller just created.
// The caller checked EnsurePrimaryReplaceable before creating it.
func (s *Service) PublishPrimaryChat(agentKey string, chatID string) error {
	store, err := s.primaryStore()
	if err != nil {
		return err
	}
	_, err = s.publishPrimary(store, agentKey, chatID)
	return err
}

func (s *Service) publishPrimary(store chat.PrimaryStore, agentKey string, chatID string) (PrimaryResult, error) {
	change, err := store.SetChatPrimary(agentKey, chatID)
	result := PrimaryResult{AgentKey: change.AgentKey, ChatID: change.ChatID, PreviousChatID: change.PreviousChatID, Changed: change.Changed}
	if err != nil {
		return result, err
	}
	if change.Changed && s.Notifications != nil {
		payload := map[string]any{"agentKey": change.AgentKey, "chatId": change.ChatID}
		if change.PreviousChatID != "" {
			payload["previousChatId"] = change.PreviousChatID
		}
		s.Notifications.Broadcast(PrimaryChangedEvent, payload)
	}
	return result, nil
}

func (s *Service) ensureChatIdle(agentKey string, chatID string) error {
	if chatID == "" || s.Runs == nil {
		return nil
	}
	activeRun, ok, err := s.Runs.ActiveRunForChat(chatID)
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(err, &conflictErr) {
		return &chat.PrimaryChatBusyError{AgentKey: agentKey, ChatID: chatID, RunIDs: append([]string(nil), conflictErr.RunIDs...)}
	}
	if err != nil {
		return err
	}
	if ok {
		return &chat.PrimaryChatBusyError{AgentKey: agentKey, ChatID: chatID, RunIDs: []string{activeRun.RunID}}
	}
	return nil
}
