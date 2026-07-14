package chat

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// SQLite chat index
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
			AGENT_MODE_       TEXT NOT NULL DEFAULT '',
			TEAM_ID_          TEXT,
			SOURCE_           TEXT NOT NULL DEFAULT '',
			SOURCE_CHANNEL_   TEXT NOT NULL DEFAULT '',
			CREATED_AT_       INTEGER NOT NULL,
			UPDATED_AT_       INTEGER NOT NULL,
			LAST_RUN_AT_      INTEGER NOT NULL DEFAULT 0,
			LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			READ_RUN_ID_      TEXT NOT NULL DEFAULT '',
			READ_AT_          INTEGER,
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_CACHED_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_REASONING_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_PROMPT_CACHE_HIT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_PROMPT_CACHE_MISS_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_CURRENCY_ TEXT NOT NULL DEFAULT '',
			USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_ REAL NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_ REAL NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_OUTPUT_ REAL NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_TOTAL_ REAL NOT NULL DEFAULT 0,
			USAGE_LLM_CHAT_COMPLETION_COUNT_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOOL_CALL_COUNT_ INTEGER NOT NULL DEFAULT 0,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_FIRST_TOKEN_LATENCY_COUNT_ INTEGER NOT NULL DEFAULT 0,
			USAGE_GENERATION_DURATION_MS_ INTEGER NOT NULL DEFAULT 0,
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
			AGENT_MODE_              TEXT NOT NULL DEFAULT '',
			TEAM_ID_                 TEXT,
			INITIAL_MESSAGE_         TEXT NOT NULL DEFAULT '',
			ASSISTANT_TEXT_          TEXT NOT NULL DEFAULT '',
			FINISH_REASON_           TEXT NOT NULL DEFAULT '',
			STARTED_AT_              INTEGER NOT NULL DEFAULT 0,
			COMPLETED_AT_            INTEGER NOT NULL,
			USAGE_PROMPT_TOKENS_     INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_      INTEGER NOT NULL DEFAULT 0,
			USAGE_CACHED_TOKENS_     INTEGER NOT NULL DEFAULT 0,
			USAGE_REASONING_TOKENS_  INTEGER NOT NULL DEFAULT 0,
			USAGE_PROMPT_CACHE_HIT_TOKENS_  INTEGER NOT NULL DEFAULT 0,
			USAGE_PROMPT_CACHE_MISS_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_CURRENCY_ TEXT NOT NULL DEFAULT '',
			USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_ REAL NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_ REAL NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_OUTPUT_ REAL NOT NULL DEFAULT 0,
			USAGE_ESTIMATED_COST_TOTAL_ REAL NOT NULL DEFAULT 0,
			USAGE_MODEL_KEY_ TEXT NOT NULL DEFAULT '',
			USAGE_LLM_CHAT_COMPLETION_COUNT_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOOL_CALL_COUNT_ INTEGER NOT NULL DEFAULT 0,
			USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_FIRST_TOKEN_LATENCY_COUNT_ INTEGER NOT NULL DEFAULT 0,
			USAGE_GENERATION_DURATION_MS_ INTEGER NOT NULL DEFAULT 0,
			FEEDBACK_TYPE_           TEXT NOT NULL DEFAULT '',
			FEEDBACK_COMMENT_        TEXT NOT NULL DEFAULT '',
			FEEDBACK_AT_             INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS IDX_RUNS_CHAT_ID_ ON RUNS(CHAT_ID_);
	`)
	if err != nil {
		return fmt.Errorf("create chats table: %w", err)
	}

	if err := s.migrateRemoveOwnerTypeColumns(); err != nil {
		return err
	}
	s.migrateAddUsageColumns()
	if err := s.migrateAwaitingColumns(); err != nil {
		return err
	}
	if err := s.migrateReadStateColumns(); err != nil {
		return err
	}
	s.migrateSourceColumn()
	s.migrateSourceChannelColumn()
	s.migrateLastRunAtColumn()
	s.migrateDetailedUsageColumns()
	s.migrateAgentModeColumns()
	return nil
}

func (s *FileStore) migrateAgentModeColumns() {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN TEAM_ID_ TEXT")
	_, _ = s.db.Exec("ALTER TABLE RUNS ADD COLUMN TEAM_ID_ TEXT")
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN AGENT_MODE_ TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("ALTER TABLE RUNS ADD COLUMN AGENT_MODE_ TEXT NOT NULL DEFAULT ''")
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_MODE_UPDATED_ ON CHATS(AGENT_MODE_, UPDATED_AT_ DESC, CHAT_ID_ DESC)")
}

// migrateLastRunAtColumn intentionally leaves existing rows at the schema
// default (zero). Historical records are not backfilled from run IDs, run
// rows, or updatedAt: public archive reads will reject them under the strict
// time contract instead of silently inventing a last-run instant.
func (s *FileStore) migrateLastRunAtColumn() {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN LAST_RUN_AT_ INTEGER NOT NULL DEFAULT 0")
}

func (s *FileStore) migrateRemoveOwnerTypeColumns() error {
	return dropOwnerTypeColumns(s.db, "CHATS", "RUNS")
}

func dropOwnerTypeColumns(db *sql.DB, tables ...string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin owner type migration: %w", err)
	}
	for _, table := range tables {
		hasColumn, err := tableHasColumn(tx, table, "OWNER_TYPE_")
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if !hasColumn {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE " + table + " DROP COLUMN OWNER_TYPE_"); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("drop %s.OWNER_TYPE_: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit owner type migration: %w", err)
	}
	return nil
}

type tableInfoQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func tableHasColumn(queryer tableInfoQuerier, table string, column string) (bool, error) {
	rows, err := queryer.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *FileStore) migrateAddUsageColumns() {
	for _, col := range []string{"USAGE_PROMPT_TOKENS_", "USAGE_COMPLETION_TOKENS_", "USAGE_TOTAL_TOKENS_"} {
		_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE CHATS ADD COLUMN %s INTEGER NOT NULL DEFAULT 0", col))
	}
}

func (s *FileStore) migrateDetailedUsageColumns() {
	intColumns := []string{
		"USAGE_CACHED_TOKENS_",
		"USAGE_REASONING_TOKENS_",
		"USAGE_PROMPT_CACHE_HIT_TOKENS_",
		"USAGE_PROMPT_CACHE_MISS_TOKENS_",
		"USAGE_LLM_CHAT_COMPLETION_COUNT_",
		"USAGE_TOOL_CALL_COUNT_",
		"USAGE_FIRST_TOKEN_LATENCY_TOTAL_MS_",
		"USAGE_FIRST_TOKEN_LATENCY_COUNT_",
		"USAGE_GENERATION_DURATION_MS_",
	}
	textColumns := []string{
		"USAGE_ESTIMATED_COST_CURRENCY_",
	}
	realColumns := []string{
		"USAGE_ESTIMATED_COST_INPUT_CACHE_HIT_",
		"USAGE_ESTIMATED_COST_INPUT_CACHE_MISS_",
		"USAGE_ESTIMATED_COST_OUTPUT_",
		"USAGE_ESTIMATED_COST_TOTAL_",
	}
	for _, table := range []string{"CHATS", "RUNS"} {
		for _, col := range intColumns {
			_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s INTEGER NOT NULL DEFAULT 0", table, col))
		}
		for _, col := range textColumns {
			_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT ''", table, col))
		}
		for _, col := range realColumns {
			_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s REAL NOT NULL DEFAULT 0", table, col))
		}
	}
	_, _ = s.db.Exec("ALTER TABLE RUNS ADD COLUMN USAGE_MODEL_KEY_ TEXT NOT NULL DEFAULT ''")
}

func (s *FileStore) migrateSourceChannelColumn() {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN SOURCE_CHANNEL_ TEXT NOT NULL DEFAULT ''")
}

func (s *FileStore) migrateSourceColumn() {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN SOURCE_ TEXT NOT NULL DEFAULT ''")
}

func (s *FileStore) migrateAwaitingColumns() error {
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

func (s *FileStore) migrateReadStateColumns() error {
	_, _ = s.db.Exec("ALTER TABLE CHATS ADD COLUMN READ_RUN_ID_ TEXT NOT NULL DEFAULT ''")
	_, err := s.db.Exec("CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_KEY_ ON CHATS(AGENT_KEY_)")
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
