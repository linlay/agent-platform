package conversationexport

import "agent-platform/internal/chat"

type ConversationReader interface {
	Summary(chatID string) (*chat.Summary, error)
	LoadConversationHistory(chatID string) (chat.Detail, error)
	PublishedArtifacts(chatID string) ([]chat.ArtifactManifestItem, error)
	ChatDir(chatID string) string
}

// Service is the read-only application boundary for conversation exports.
type Service struct {
	Chats            ConversationReader
	ResolveAssistant ResolveAssistant
}

func (s Service) Snapshot(chatID string, capturedAt int64, locale string) (SnapshotDocument, error) {
	summary, detail, err := s.loadConversation(chatID)
	if err != nil {
		return SnapshotDocument{}, err
	}
	artifacts, err := s.Chats.PublishedArtifacts(chatID)
	if err != nil {
		return SnapshotDocument{}, err
	}
	attachments, err := BuildSnapshotAttachments(chatID, artifacts, s.Chats.ChatDir(chatID))
	if err != nil {
		return SnapshotDocument{}, err
	}
	return BuildSnapshotDocument(summary, detail.Events, attachments, capturedAt, locale, s.ResolveAssistant)
}

func (s Service) Markdown(chatID string, capturedAt int64, locale string) ([]byte, string, error) {
	summary, detail, err := s.loadConversation(chatID)
	if err != nil {
		return nil, "", err
	}
	document, err := BuildSnapshotDocument(summary, detail.Events, nil, capturedAt, locale, s.ResolveAssistant)
	if err != nil {
		return nil, "", err
	}
	body, err := RenderMarkdown(document.Snapshot)
	return body, document.Snapshot.Title, err
}

func (s Service) loadConversation(chatID string) (*chat.Summary, chat.Detail, error) {
	summary, err := s.Chats.Summary(chatID)
	if err != nil {
		return nil, chat.Detail{}, err
	}
	if summary == nil {
		return nil, chat.Detail{}, chat.ErrChatNotFound
	}
	detail, err := s.Chats.LoadConversationHistory(chatID)
	return summary, detail, err
}
