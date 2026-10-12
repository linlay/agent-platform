package chat

import (
	"database/sql"
	"errors"
	"os"
	"strings"
)

// LoadRunRecord finds a Run by ID alone. It returns nil when the Run has no
// lifecycle row, which includes hidden Runs that are never persisted.
func (s *FileStore) LoadRunRecord(runID string) (*RunRecord, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record := RunRecord{RunID: runID}
	err := s.db.QueryRow(`SELECT CHAT_ID_, AGENT_KEY_, STARTED_AT_, COMPLETED_AT_, FINISH_REASON_ FROM RUNS WHERE RUN_ID_=?`, runID).
		Scan(&record.ChatID, &record.AgentKey, &record.StartedAt, &record.CompletedAt, &record.FinishReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines, err := readPersistedJSONLines(s.chatJSONLPath(record.ChatID))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if query := loadRunQueryFromLines(lines, record.ChatID, runID); query != nil {
		record.AccessLevel = strings.TrimSpace(stringValue(query.Query["accessLevel"]))
	}
	return &record, nil
}
