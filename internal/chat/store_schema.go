package chat

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// SQLite index (replaces index.json, matching Java chats.db)
// ---------------------------------------------------------------------------

func (s *FileStore) initDB() error {
	dbPath := filepath.Join(s.root, "chats.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open chats.db: %w", err)
	}
	s.db = db

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS CHATS (
			CHAT_ID_          TEXT PRIMARY KEY,
			CHAT_NAME_        TEXT NOT NULL,
			AGENT_KEY_        TEXT NOT NULL DEFAULT '',
			TEAM_ID_          TEXT,
			SOURCE_CHANNEL_   TEXT NOT NULL DEFAULT '',
			CREATED_AT_       INTEGER NOT NULL,
			UPDATED_AT_       INTEGER NOT NULL,
			LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			READ_RUN_ID_      TEXT NOT NULL DEFAULT '',
			READ_AT_          INTEGER,
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			AWAITING_ID_ TEXT NOT NULL DEFAULT '',
			AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
			AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
			AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS IDX_CHATS_LAST_RUN_ID_ ON CHATS(LAST_RUN_ID_);
		CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_KEY_ ON CHATS(AGENT_KEY_);
		CREATE TABLE IF NOT EXISTS RUNS (
			RUN_ID_                  TEXT PRIMARY KEY,
			CHAT_ID_                 TEXT NOT NULL,
			AGENT_KEY_               TEXT NOT NULL DEFAULT '',
			INITIAL_MESSAGE_         TEXT NOT NULL DEFAULT '',
			ASSISTANT_TEXT_          TEXT NOT NULL DEFAULT '',
			FINISH_REASON_           TEXT NOT NULL DEFAULT '',
			STARTED_AT_              INTEGER NOT NULL DEFAULT 0,
			COMPLETED_AT_            INTEGER NOT NULL,
			USAGE_PROMPT_TOKENS_     INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_      INTEGER NOT NULL DEFAULT 0,
			FEEDBACK_TYPE_           TEXT NOT NULL DEFAULT '',
			FEEDBACK_COMMENT_        TEXT NOT NULL DEFAULT '',
			FEEDBACK_AT_             INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS IDX_RUNS_CHAT_ID_ ON RUNS(CHAT_ID_);
	`)
	if err != nil {
		return fmt.Errorf("create chats table: %w", err)
	}

	s.migrateAddUsageColumns()
	if err := s.migrateAwaitingColumns(); err != nil {
		return err
	}
	if err := s.migrateReadStateColumns(); err != nil {
		return err
	}
	s.migrateSourceChannelColumn()

	// Migrate from index.json if it exists and DB is empty
	s.migrateFromIndexJSON()
	return nil
}

func (s *FileStore) migrateAddUsageColumns() {
	for _, col := range []string{"USAGE_PROMPT_TOKENS_", "USAGE_COMPLETION_TOKENS_", "USAGE_TOTAL_TOKENS_"} {
		_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE CHATS ADD COLUMN %s INTEGER NOT NULL DEFAULT 0", col))
	}
}

func (s *FileStore) migrateSourceChannelColumn() {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN SOURCE_CHANNEL_ TEXT NOT NULL DEFAULT ''")
}

func (s *FileStore) migrateAwaitingColumns() error {
	hasLegacyAwaiting, err := s.tableHasColumn("CHATS", "PENDING_AWAITING_ID_")
	if err != nil {
		return err
	}
	if hasLegacyAwaiting {
		hasReadStatus, err := s.tableHasColumn("CHATS", "READ_STATUS_")
		if err != nil {
			return err
		}
		return s.rebuildChatsTableAwaitingColumns(hasReadStatus)
	}
	for _, stmt := range []string{
		"ALTER TABLE CHATS ADD COLUMN AWAITING_ID_ TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE CHATS ADD COLUMN AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE CHATS ADD COLUMN AWAITING_MODE_ TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE CHATS ADD COLUMN AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0",
	} {
		_, _ = s.db.Exec(stmt)
	}
	return nil
}

func (s *FileStore) rebuildChatsTableAwaitingColumns(includeReadStatus bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DROP TABLE IF EXISTS CHATS_V2`); err != nil {
		return err
	}
	if includeReadStatus {
		if _, err = tx.Exec(`
			CREATE TABLE CHATS_V2 (
				CHAT_ID_          TEXT PRIMARY KEY,
				CHAT_NAME_        TEXT NOT NULL,
				AGENT_KEY_        TEXT NOT NULL DEFAULT '',
				TEAM_ID_          TEXT,
				CREATED_AT_       INTEGER NOT NULL,
				UPDATED_AT_       INTEGER NOT NULL,
				LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
				LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
				READ_STATUS_      INTEGER NOT NULL DEFAULT 0,
				READ_AT_          INTEGER,
				USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
				USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
				USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
				AWAITING_ID_ TEXT NOT NULL DEFAULT '',
				AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
				AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
				AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
			)`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO CHATS_V2 (
				CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
				LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
				AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_
			)
			SELECT
				CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
				LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_STATUS_, READ_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
				PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_
			FROM CHATS`); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(`
			CREATE TABLE CHATS_V2 (
				CHAT_ID_          TEXT PRIMARY KEY,
				CHAT_NAME_        TEXT NOT NULL,
				AGENT_KEY_        TEXT NOT NULL DEFAULT '',
				TEAM_ID_          TEXT,
				CREATED_AT_       INTEGER NOT NULL,
				UPDATED_AT_       INTEGER NOT NULL,
				LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
				LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
				READ_RUN_ID_      TEXT NOT NULL DEFAULT '',
				READ_AT_          INTEGER,
				USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
				USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
				USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
				AWAITING_ID_ TEXT NOT NULL DEFAULT '',
				AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
				AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
				AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
			)`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO CHATS_V2 (
				CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
				LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
				AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_
			)
			SELECT
				CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
				LAST_RUN_ID_, LAST_RUN_CONTENT_, COALESCE(READ_RUN_ID_, ''), READ_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
				PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_
			FROM CHATS`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DROP TABLE CHATS`); err != nil {
		return err
	}
	if _, err = tx.Exec(`ALTER TABLE CHATS_V2 RENAME TO CHATS`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS IDX_CHATS_LAST_RUN_ID_ ON CHATS(LAST_RUN_ID_)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_KEY_ ON CHATS(AGENT_KEY_)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FileStore) migrateReadStateColumns() error {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN READ_RUN_ID_ TEXT NOT NULL DEFAULT ''")
	hasReadStatus, err := s.tableHasColumn("CHATS", "READ_STATUS_")
	if err != nil {
		return err
	}
	if hasReadStatus {
		if _, err := s.db.Exec(`UPDATE CHATS
			SET READ_RUN_ID_ = LAST_RUN_ID_
			WHERE READ_RUN_ID_ = ''
				AND READ_STATUS_ = 1
				AND LAST_RUN_ID_ != ''`); err != nil {
			return err
		}
		return s.rebuildChatsTableWithoutReadStatus()
	}
	_, err = s.db.Exec("CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_KEY_ ON CHATS(AGENT_KEY_)")
	return err
}

func (s *FileStore) rebuildChatsTableWithoutReadStatus() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DROP TABLE IF EXISTS CHATS_V2`); err != nil {
		return err
	}
	if _, err = tx.Exec(`
		CREATE TABLE CHATS_V2 (
			CHAT_ID_          TEXT PRIMARY KEY,
			CHAT_NAME_        TEXT NOT NULL,
			AGENT_KEY_        TEXT NOT NULL DEFAULT '',
			TEAM_ID_          TEXT,
			CREATED_AT_       INTEGER NOT NULL,
			UPDATED_AT_       INTEGER NOT NULL,
			LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			READ_RUN_ID_      TEXT NOT NULL DEFAULT '',
			READ_AT_          INTEGER,
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			AWAITING_ID_ TEXT NOT NULL DEFAULT '',
			AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
			AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
			AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO CHATS_V2 (
			CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
			AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_
		)
		SELECT
			CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_, COALESCE(READ_RUN_ID_, ''), READ_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
			AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_
		FROM CHATS`); err != nil {
		return err
	}
	if _, err = tx.Exec(`DROP TABLE CHATS`); err != nil {
		return err
	}
	if _, err = tx.Exec(`ALTER TABLE CHATS_V2 RENAME TO CHATS`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS IDX_CHATS_LAST_RUN_ID_ ON CHATS(LAST_RUN_ID_)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_KEY_ ON CHATS(AGENT_KEY_)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FileStore) tableHasColumn(table string, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *FileStore) migrateFromIndexJSON() {
	path := filepath.Join(s.root, "index.json")
	file, err := os.Open(path)
	if err != nil {
		return // no index.json, nothing to migrate
	}
	defer file.Close()

	var count int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM CHATS").Scan(&count)
	if count > 0 {
		return // DB already has data
	}

	type legacySummary struct {
		ChatID         string `json:"chatId"`
		ChatName       string `json:"chatName"`
		AgentKey       string `json:"agentKey"`
		TeamID         string `json:"teamId"`
		CreatedAt      int64  `json:"createdAt"`
		UpdatedAt      int64  `json:"updatedAt"`
		LastRunID      string `json:"lastRunId"`
		LastRunContent string `json:"lastRunContent"`
		ReadStatus     int    `json:"readStatus"`
		ReadAt         *int64 `json:"readAt"`
	}
	var summaries map[string]legacySummary
	if err := json.NewDecoder(file).Decode(&summaries); err != nil || len(summaries) == 0 {
		return
	}
	for _, sum := range summaries {
		readRunID := ""
		if sum.ReadStatus == 1 {
			readRunID = sum.LastRunID
		}
		_, _ = s.db.Exec(`INSERT OR IGNORE INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sum.ChatID, sum.ChatName, sum.AgentKey, nilIfEmpty(sum.TeamID),
			sum.CreatedAt, sum.UpdatedAt, sum.LastRunID, sum.LastRunContent,
			readRunID, sum.ReadAt)
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
