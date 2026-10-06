package chat

import (
	"context"
	"fmt"
)

// MemoryChats enumerates managed archives without restoring them into active chats.
func (s *ArchiveStore) MemoryChats(ctx context.Context, since, after int64, afterID string, limit int) ([]MemoryChat, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("invalid memory page size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT c.CHAT_ID_, MAX(r.COMPLETED_AT_), COUNT(r.RUN_ID_)
 FROM ARCHIVED_CHATS c JOIN ARCHIVED_RUNS r ON r.CHAT_ID_=c.CHAT_ID_
 WHERE COALESCE(c.TEAM_ID_,'')='' AND c.SOURCE_ NOT LIKE 'automation:%' AND c.SOURCE_ NOT LIKE 'run-query:%'
 AND NOT EXISTS(SELECT 1 FROM ARCHIVED_RUNS active WHERE active.CHAT_ID_=c.CHAT_ID_ AND active.COMPLETED_AT_=0)
 GROUP BY c.CHAT_ID_ HAVING MAX(r.COMPLETED_AT_)>=? AND (MAX(r.COMPLETED_AT_)>? OR (MAX(r.COMPLETED_AT_)=? AND c.CHAT_ID_>?))
 ORDER BY MAX(r.COMPLETED_AT_),c.CHAT_ID_ LIMIT ?`, since, after, after, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MemoryChat
	for rows.Next() {
		var c MemoryChat
		if err = rows.Scan(&c.ChatID, &c.CompletedAt, &c.RunCount); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *ArchiveStore) ListRuns(chatID string) ([]RunSummary, error) {
	if !ValidChatID(chatID) {
		return nil, ErrChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listRunsLocked(chatID)
}
func (s *ArchiveStore) MemoryMessages(chatID, runID string) ([]MemoryMessage, error) {
	if !ValidChatID(chatID) {
		return nil, ErrChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var content string
	// Bound before reading large blobs; the limit matches active Chat evidence.
	err := s.db.QueryRow(`SELECT JSONL_CONTENT_ FROM ARCHIVED_CHATS WHERE CHAT_ID_=? AND length(CAST(JSONL_CONTENT_ AS BLOB))<=?`, chatID, 64<<20).Scan(&content)
	if err != nil {
		return nil, err
	}
	lines, err := readJSONLinesContent(content)
	if err != nil {
		return nil, err
	}
	return memoryMessagesFromLines(lines, runID), nil
}
