// Package conversation coordinates Chat and Archive use cases while the chat
// package remains the persistence/replay source of truth.
package conversation

import (
	"errors"
	"os"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

var ErrNotConfigured = errors.New("conversation service is not configured")

type Service struct {
	Chats    chat.Store
	Archives *chat.ArchiveStore
	Archiver *chat.Archiver
	Runs     contracts.RunManager
}

func NewService(chats chat.Store, archives *chat.ArchiveStore, archiver *chat.Archiver, runs contracts.RunManager) *Service {
	return &Service{Chats: chats, Archives: archives, Archiver: archiver, Runs: runs}
}

func (s *Service) ListSummaries(lastRunID string, agentKey string, agentModes []string, limit int) ([]chat.Summary, error) {
	if s == nil || s.Chats == nil {
		return nil, ErrNotConfigured
	}
	return s.Chats.ListChatsWithAgentModesAndLimit(lastRunID, agentKey, agentModes, limit)
}

// ListSummariesWithPinned keeps filtering and truncation in the persistence layer.
func (s *Service) ListSummariesWithPinned(lastRunID, agentKey string, modes []string, limit int, pinned *bool) ([]chat.Summary, error) {
	if pinned == nil {
		return s.ListSummaries(lastRunID, agentKey, modes, limit)
	}
	if s == nil || s.Chats == nil {
		return nil, ErrNotConfigured
	}
	store, ok := s.Chats.(chat.PinnedListStore)
	if !ok {
		return nil, errors.New("chat pin filtering is not supported")
	}
	return store.ListChatsWithOptions(chat.ListOptions{LastRunID: lastRunID, AgentKey: agentKey, AgentModes: modes, Limit: limit, Pinned: pinned})
}

func (s *Service) RecentSummaries(agentKey, teamID string, limit int, pinned *bool) ([]chat.Summary, error) {
	if s == nil || s.Chats == nil {
		return nil, ErrNotConfigured
	}
	if pinned == nil {
		if teamID != "" {
			return s.Chats.RecentChatsByTeam(teamID, limit)
		}
		return s.Chats.RecentChatsByAgent(agentKey, limit)
	}
	store, ok := s.Chats.(chat.PinnedListStore)
	if !ok {
		return nil, errors.New("chat pin filtering is not supported")
	}
	return store.RecentChatsByOwner(agentKey, teamID, limit, pinned)
}

func (s *Service) ActiveRun(chatID string) (contracts.RunStatusInfo, bool, error) {
	if s == nil || s.Runs == nil {
		return contracts.RunStatusInfo{}, false, nil
	}
	return s.Runs.ActiveRunForChat(chatID)
}

type ArchiveResult struct {
	ChatID   string
	AgentKey string
	Success  bool
	Error    string
}

type RestoreResult struct {
	ChatID  string
	Success bool
	Summary *chat.Summary
	Error   string
}

func (s *Service) ArchiveChats(chatIDs []string) ([]ArchiveResult, error) {
	if s == nil || s.Archiver == nil {
		return nil, errors.New("archiver is not configured")
	}
	if len(chatIDs) == 0 {
		return nil, errors.New("chatIds is required")
	}
	results := make([]ArchiveResult, 0, len(chatIDs))
	for _, rawChatID := range chatIDs {
		chatID := strings.TrimSpace(rawChatID)
		result := ArchiveResult{ChatID: chatID}
		if !chat.ValidChatID(chatID) {
			result.Error = "invalid chatId"
			results = append(results, result)
			continue
		}
		if err := s.EnsureNoActiveRun(chatID); err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		if err := s.Archiver.ArchiveChat(chatID); err != nil {
			if isArchiveResultError(err) {
				result.Error = archiveResultError(err)
				results = append(results, result)
				continue
			}
			return nil, err
		}
		result.Success = true
		if s.Archives != nil {
			archived, err := s.Archives.LoadArchived(chatID)
			if err != nil {
				return nil, err
			}
			if archived != nil {
				result.AgentKey = archived.Summary.AgentKey
			}
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) RestoreArchives(chatIDs []string) ([]RestoreResult, error) {
	if s == nil || s.Archiver == nil {
		return nil, errors.New("archiver is not configured")
	}
	if len(chatIDs) == 0 {
		return nil, errors.New("chatIds is required")
	}
	results := make([]RestoreResult, 0, len(chatIDs))
	for _, rawChatID := range chatIDs {
		chatID := strings.TrimSpace(rawChatID)
		result := RestoreResult{ChatID: chatID}
		if !chat.ValidChatID(chatID) {
			result.Error = "invalid chatId"
			results = append(results, result)
			continue
		}
		summary, err := s.Archiver.RestoreChat(chatID)
		if err != nil {
			if isRestoreResultError(err) {
				result.Error = restoreResultError(err)
				results = append(results, result)
				continue
			}
			return nil, err
		}
		result.Success = true
		result.Summary = &summary
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) EnsureNoActiveRun(chatID string) error {
	if s == nil || s.Runs == nil {
		return nil
	}
	activeRun, ok, err := s.Runs.ActiveRunForChat(chatID)
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(err, &conflictErr) {
		return errors.New("active run conflict")
	}
	if err != nil {
		return err
	}
	if ok || strings.TrimSpace(activeRun.RunID) != "" {
		return errors.New("active run conflict")
	}
	return nil
}

func (s *Service) ListArchives(agentKey string, limit, offset int) ([]chat.ArchivedSummary, int, error) {
	if s == nil || s.Archives == nil {
		return nil, 0, errors.New("archive store is not configured")
	}
	return s.Archives.ListArchived(agentKey, limit, offset)
}

func (s *Service) LoadArchive(chatID string) (*chat.ArchivedChat, error) {
	if s == nil || s.Archives == nil {
		return nil, errors.New("archive store is not configured")
	}
	return s.Archives.LoadArchived(chatID)
}

func (s *Service) SearchArchives(query, agentKey string, limit int) ([]chat.ArchiveSearchHit, error) {
	if s == nil || s.Archives == nil {
		return nil, errors.New("archive store is not configured")
	}
	return s.Archives.SearchArchived(query, agentKey, limit)
}

func (s *Service) DeleteArchive(chatID string) error {
	if s == nil || s.Archives == nil {
		return errors.New("archive store is not configured")
	}
	chatID = strings.TrimSpace(chatID)
	if !chat.ValidChatID(chatID) {
		return os.ErrPermission
	}
	return s.Archives.DeleteArchived(chatID)
}

func isArchiveResultError(err error) bool {
	return errors.Is(err, chat.ErrChatNotFound) || errors.Is(err, chat.ErrChatAlreadyArchived) || errors.Is(err, os.ErrPermission)
}

func archiveResultError(err error) string {
	switch {
	case errors.Is(err, chat.ErrChatNotFound):
		return "chat not found"
	case errors.Is(err, chat.ErrChatAlreadyArchived):
		return "already archived"
	case errors.Is(err, os.ErrPermission):
		return "invalid chatId"
	default:
		return err.Error()
	}
}

func isRestoreResultError(err error) bool {
	return errors.Is(err, chat.ErrChatNotFound) || errors.Is(err, chat.ErrChatAlreadyActive) || errors.Is(err, os.ErrPermission)
}

func restoreResultError(err error) string {
	switch {
	case errors.Is(err, chat.ErrChatNotFound):
		return "archive not found"
	case errors.Is(err, chat.ErrChatAlreadyActive):
		return "active chat already exists"
	case errors.Is(err, os.ErrPermission):
		return "invalid chatId"
	default:
		return err.Error()
	}
}
