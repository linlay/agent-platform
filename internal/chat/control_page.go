package chat

import (
	"database/sql"
	"os"
	"path/filepath"
)

// ControlChatIDs uses a stable identity order independent of UI pin/manual order.
// A cursor contains only the last scanned identity, never the complete catalog.
func controlChatIDs(db *sql.DB, table, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := db.Query("SELECT CHAT_ID_ FROM "+table+" WHERE CHAT_ID_ > ? ORDER BY CHAT_ID_ LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *FileStore) ControlChatIDs(after string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return controlChatIDs(s.db, "CHATS", after, limit)
}
func (s *ArchiveStore) ControlChatIDs(after string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return controlChatIDs(s.db, "ARCHIVED_CHATS", after, limit)
}
func (s *FileStore) ControlHistorySize(id string) (int64, error) {
	if !ValidChatID(id) {
		return 0, os.ErrPermission
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(s.ChatDir(id)), id+".jsonl"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *ArchiveStore) ControlSummary(id string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sum Summary
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), COALESCE(SOURCE_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_ FROM ARCHIVED_CHATS WHERE CHAT_ID_=?", id).Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.Source, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID)
	if err == sql.ErrNoRows {
		return nil, ErrChatNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sum, nil
}
func (s *ArchiveStore) ControlHistorySize(id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var size int64
	err := s.db.QueryRow("SELECT length(CAST(JSONL_CONTENT_ AS BLOB))+length(CAST(EVENTS_CONTENT_ AS BLOB))+length(CAST(RAW_MESSAGES_CONTENT_ AS BLOB)) FROM ARCHIVED_CHATS WHERE CHAT_ID_=?", id).Scan(&size)
	return size, err
}
