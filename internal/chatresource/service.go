// Package chatresource owns Chat upload, artifact, reference, and image
// mutation coordination independently of the transport adapters.
package chatresource

import (
	"errors"
	"strings"
	"sync"

	"agent-platform/internal/chat"
)

var (
	ErrNotConfigured = errors.New("chat resource service is not configured")
	ErrOwnerMismatch = errors.New("resource owner mismatch")
)

type Service struct {
	uploadMutation sync.Mutex
	chats          chat.Store
}

func NewService(chats chat.Store) *Service { return &Service{chats: chats} }

func (s *Service) LockUploadMutation() func() {
	if s == nil {
		return func() {}
	}
	s.uploadMutation.Lock()
	return s.uploadMutation.Unlock
}

func (s *Service) ResolveResource(file string) (string, error) {
	if s == nil || s.chats == nil {
		return "", ErrNotConfigured
	}
	return s.chats.ResolveResource(file)
}

type ImageCommitCommand struct {
	AgentKey         string
	ChatID           string
	Profile          string
	ResourceID       string
	RelativePath     string
	Mode             string
	ExpectedRevision string
	MIMEType         string
	Data             []byte
}

func (s *Service) CommitImage(command ImageCommitCommand) (chat.ResourceDocumentCommitResult, error) {
	if s == nil || s.chats == nil {
		return chat.ResourceDocumentCommitResult{}, ErrNotConfigured
	}
	summary, err := s.chats.Summary(command.ChatID)
	if err != nil {
		return chat.ResourceDocumentCommitResult{}, err
	}
	if summary == nil {
		return chat.ResourceDocumentCommitResult{}, chat.ErrChatNotFound
	}
	if strings.TrimSpace(summary.TeamID) != "" || strings.TrimSpace(summary.AgentKey) == "" || strings.TrimSpace(summary.AgentKey) != strings.TrimSpace(command.AgentKey) {
		return chat.ResourceDocumentCommitResult{}, ErrOwnerMismatch
	}
	committer, ok := s.chats.(chat.ResourceDocumentCommitter)
	if !ok || committer == nil {
		return chat.ResourceDocumentCommitResult{}, ErrNotConfigured
	}
	return committer.CommitResourceDocument(chat.ResourceDocumentCommitRequest{
		ChatID: command.ChatID, Profile: command.Profile, ResourceID: command.ResourceID,
		RelativePath: command.RelativePath, Mode: command.Mode, ExpectedRevision: command.ExpectedRevision,
		DocumentKind: "document-image", MIMEType: command.MIMEType, Data: append([]byte(nil), command.Data...),
	})
}
