package chat

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"agent-platform/internal/sqlitecontract"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// SQLite chat index
// ---------------------------------------------------------------------------

func (s *FileStore) initDB(startupAdopt bool) error {
	dbPath := filepath.Join(s.root, "chats.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open chats.db: %w", err)
	}
	s.db = db

	initialize := sqlitecontract.InitializeOrVerify
	if startupAdopt {
		initialize = sqlitecontract.InitializeOrVerifyAtStartup
	}
	return initialize(db, dbPath, s.root, chatSchemaSpec, func() (bool, error) {
		// Archive storage is initialized independently and verifies its own
		// schema. Its directory must not make an otherwise fresh chat store fail.
		return sqlitecontract.HasResidualData(s.root, "archive")
	}, func() error {
		return createChatSchema(db)
	})
}

func createChatSchema(db *sql.DB) error {
	_, err := db.Exec(`
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
		CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_MODE_UPDATED_ ON CHATS(AGENT_MODE_, UPDATED_AT_ DESC, CHAT_ID_ DESC);
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
	return nil
}

var chatSchemaSpec = sqlitecontract.Spec{
	Name:          "chat-v1",
	ApplicationID: 0x41504348, // APCH
	UserVersion:   1,
	Objects: []sqlitecontract.Object{
		{Type: "table", Name: "CHATS"},
		{Type: "table", Name: "RUNS"},
		{Type: "index", Name: "IDX_CHATS_LAST_RUN_ID_"},
		{Type: "index", Name: "IDX_CHATS_AGENT_KEY_"},
		{Type: "index", Name: "IDX_CHATS_AGENT_MODE_UPDATED_"},
		{Type: "index", Name: "IDX_RUNS_CHAT_ID_"},
	},
	ForbiddenColumns: []sqlitecontract.Column{
		{Table: "CHATS", Name: "OWNER_TYPE_"},
		{Table: "RUNS", Name: "OWNER_TYPE_"},
	},
	BuildCanonical: createChatSchema,
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
