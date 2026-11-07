package query

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	sessionbuild "agent-platform/internal/runtime/session"
)

func (s *Service) PrimaryChatEnabledForAgent(key string) bool {
	def, ok := s.deps.Registry.AgentDefinition(key)
	return ok && s.deps.Config.PrimaryChat.Enabled && sessionbuild.PrimaryChatEligible(def)
}

func (s *Service) primaryChatRole(def catalog.AgentDefinition, chatID string, creatingPrimary bool) (bool, error) {
	if !s.deps.Config.PrimaryChat.Enabled || !sessionbuild.PrimaryChatEligible(def) {
		return false, nil
	}
	if creatingPrimary {
		return true, nil
	}
	store, ok := s.deps.Chats.(chat.PrimaryStore)
	if !ok {
		return false, nil
	}
	primary, err := store.PrimaryChatID(def.Key)
	return primary != "" && primary == chatID, err
}

func (s *Service) primaryModelOptions(def catalog.AgentDefinition) *queryinput.QueryModelOptions {
	key, effort := s.deps.Config.PrimaryChat.ModelKey, s.deps.Config.PrimaryChat.ReasoningEffort
	if key == "" {
		key = def.ModelKey
	}
	if effort == "" {
		effort = def.ModelReasoningEffort
	}
	return &queryinput.QueryModelOptions{Key: key, ReasoningEffort: effort}
}

// The backend-default selector inherits for new self-workers sent by a scheduling Chat.
func (s *Service) inheritsPrimaryAccess(r contracts.RunStartRequest, newChat bool) (bool, error) {
	if !newChat || !contracts.IsDefaultAccessLevelRequest(r.AccessLevel) || r.AgentKey != r.Origin.AgentKey {
		return false, nil
	}
	def, ok := s.deps.Registry.AgentDefinition(r.Origin.AgentKey)
	if !ok {
		return false, nil
	}
	return s.primaryChatRole(def, r.Origin.ChatID, false)
}
