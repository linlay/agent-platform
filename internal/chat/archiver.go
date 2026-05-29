package chat

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type Archiver struct {
	active  *FileStore
	archive *ArchiveStore
}

func NewArchiver(active *FileStore, archive *ArchiveStore) *Archiver {
	return &Archiver{active: active, archive: archive}
}

func (a *Archiver) ArchiveChat(chatID string) error {
	if a == nil || a.active == nil || a.archive == nil {
		return errors.New("archiver is not configured")
	}
	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return os.ErrPermission
	}

	summary, err := a.active.Summary(chatID)
	if err != nil {
		return err
	}
	if summary == nil {
		return ErrChatNotFound
	}
	if exists, err := a.archive.exists(chatID); err != nil {
		return err
	} else if exists {
		return ErrChatAlreadyArchived
	}
	runs, err := a.active.ListRuns(chatID)
	if err != nil {
		return err
	}
	jsonlContent, err := readFileStringIfExists(a.active.chatJSONLPath(chatID))
	if err != nil {
		return err
	}
	hasAttachments, err := chatDirHasAttachments(a.active.ChatDir(chatID))
	if err != nil {
		return err
	}
	hasChatDirContent, err := chatDirHasAnyEntries(a.active.ChatDir(chatID))
	if err != nil {
		return err
	}

	archived := ArchivedChat{
		Summary: ArchivedSummary{
			ChatID:         summary.ChatID,
			ChatName:       summary.ChatName,
			AgentKey:       summary.AgentKey,
			TeamID:         summary.TeamID,
			CreatedAt:      summary.CreatedAt,
			UpdatedAt:      summary.UpdatedAt,
			ArchivedAt:     time.Now().UnixMilli(),
			LastRunID:      summary.LastRunID,
			LastRunContent: summary.LastRunContent,
			Usage:          summary.Usage,
			HasAttachments: hasAttachments,
		},
		Runs:         runs,
		JSONLContent: jsonlContent,
	}
	if err := a.archive.ArchiveChat(archived); err != nil {
		return err
	}

	movedAttachments := false
	if hasChatDirContent {
		if err := os.Rename(a.active.ChatDir(chatID), a.archive.ChatDir(chatID)); err != nil {
			_ = a.archive.DeleteArchived(chatID)
			return fmt.Errorf("move attachments: %w", err)
		}
		movedAttachments = true
	}
	if err := a.active.DeleteChat(chatID); err != nil {
		log.Printf("[chat][archive] active cleanup failed chatId=%s movedAttachments=%t err=%v", chatID, movedAttachments, err)
	}
	return nil
}

func (a *Archiver) ArchiveBatch(chatIDs []string) []ArchiveResult {
	results := make([]ArchiveResult, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		chatID = strings.TrimSpace(chatID)
		result := ArchiveResult{ChatID: chatID}
		if err := a.ArchiveChat(chatID); err != nil {
			result.Error = archiveErrorMessage(err)
		} else {
			result.Success = true
		}
		results = append(results, result)
	}
	return results
}

func (s *ArchiveStore) exists(chatID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.existsLocked(chatID)
}

func archiveErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrChatNotFound):
		return "chat not found"
	case errors.Is(err, ErrChatAlreadyArchived):
		return "already archived"
	default:
		return err.Error()
	}
}

func readFileStringIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func chatDirHasAttachments(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == LegacyToolResultsDirName || name == ToolRootDirName {
			continue
		}
		return true, nil
	}
	return false, nil
}

func chatDirHasAnyEntries(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}
