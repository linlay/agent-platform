package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/stream"

	_ "modernc.org/sqlite"
)

var ErrChatNotFound = errors.New("chat not found")

type Store interface {
	EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error)
	Summary(chatID string) (*Summary, error)
	SetPendingAwaiting(chatID string, pending PendingAwaiting) error
	ClearPendingAwaiting(chatID string, awaitingID string) error
	AppendEvent(chatID string, event stream.EventData) error
	AppendQueryLine(chatID string, line QueryLine) error
	AppendStepLine(chatID string, line StepLine) error
	AppendEventLine(chatID string, line EventLine) error
	AppendSubmitLine(chatID string, line SubmitLine) error
	LoadRawMessages(chatID string, k int) ([]map[string]any, error)
	OnRunCompleted(completion RunCompletion) error
	ListChats(lastRunID string, agentKey string) ([]Summary, error)
	LoadChat(chatID string) (Detail, error)
	LoadRunTrace(chatID string, runID string) (RunTrace, error)
	SearchSession(chatID string, query string, limit int) ([]SearchHit, error)
	MarkRead(chatID string, runID string) (Summary, error)
	AgentChatStats() (map[string]AgentChatStats, error)
	ResolveResource(file string) (string, error)
	ChatDir(chatID string) string
}

type FileStore struct {
	root string
	mu   sync.Mutex
	db   *sql.DB
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	store := &FileStore{root: root}
	if err := store.initDB(); err != nil {
		return nil, err
	}
	return store, nil
}

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
			CREATED_AT_       INTEGER NOT NULL,
			UPDATED_AT_       INTEGER NOT NULL,
			LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			READ_RUN_ID_      TEXT NOT NULL DEFAULT '',
			READ_AT_          INTEGER,
			USAGE_PROMPT_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			PENDING_AWAITING_ID_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS IDX_CHATS_LAST_RUN_ID_ ON CHATS(LAST_RUN_ID_);
		CREATE INDEX IF NOT EXISTS IDX_CHATS_AGENT_KEY_ ON CHATS(AGENT_KEY_);
	`)
	if err != nil {
		return fmt.Errorf("create chats table: %w", err)
	}

	s.migrateAddUsageColumns()
	s.migrateAddPendingAwaitingColumns()
	if err := s.migrateReadStateColumns(); err != nil {
		return err
	}

	// Migrate from index.json if it exists and DB is empty
	s.migrateFromIndexJSON()
	return nil
}

func (s *FileStore) migrateAddUsageColumns() {
	for _, col := range []string{"USAGE_PROMPT_TOKENS_", "USAGE_COMPLETION_TOKENS_", "USAGE_TOTAL_TOKENS_"} {
		_, _ = s.db.Exec(fmt.Sprintf("ALTER TABLE CHATS ADD COLUMN %s INTEGER NOT NULL DEFAULT 0", col))
	}
}

func (s *FileStore) migrateAddPendingAwaitingColumns() {
	for _, stmt := range []string{
		"ALTER TABLE CHATS ADD COLUMN PENDING_AWAITING_ID_ TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE CHATS ADD COLUMN PENDING_AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE CHATS ADD COLUMN PENDING_AWAITING_MODE_ TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE CHATS ADD COLUMN PENDING_AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0",
	} {
		_, _ = s.db.Exec(stmt)
	}
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
			PENDING_AWAITING_ID_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_RUN_ID_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_MODE_ TEXT NOT NULL DEFAULT '',
			PENDING_AWAITING_CREATED_AT_ INTEGER NOT NULL DEFAULT 0
		)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO CHATS_V2 (
			CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
			PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_
		)
		SELECT
			CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_, COALESCE(READ_RUN_ID_, ''), READ_AT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
			PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_
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

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if exists
	var existing Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_, USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_ FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&existing.ChatID, &existing.ChatName, &existing.AgentKey, &existing.TeamID, &existing.CreatedAt, &existing.UpdatedAt, &existing.LastRunID, &existing.LastRunContent, &existing.Read.ReadRunID, &existing.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if err == nil {
		applyDerivedReadState(&existing)
		if usage.TotalTokens > 0 {
			existing.Usage = &usage
		}
		existing.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		return existing, false, nil
	}

	now := time.Now().UnixMilli()
	summary := Summary{
		ChatID:    chatID,
		ChatName:  defaultChatName(firstMessage),
		AgentKey:  agentKey,
		TeamID:    teamID,
		CreatedAt: now,
		UpdatedAt: now,
		Read: ChatReadState{
			IsRead: true,
		},
	}
	_, err = s.db.Exec(`INSERT INTO CHATS (CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_)
		VALUES (?, ?, ?, ?, ?, ?, '', '', '')`,
		chatID, summary.ChatName, agentKey, nilIfEmpty(teamID), now, now)
	if err != nil {
		return Summary{}, false, err
	}
	// Create directory for uploads/attachments
	_ = os.MkdirAll(s.ChatDir(chatID), 0o755)
	return summary, true, nil
}

// AppendEvent writes a raw SSE event to events.jsonl (legacy path).
func (s *FileStore) AppendEvent(chatID string, event stream.EventData) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), event)
}

func (s *FileStore) AppendQueryLine(chatID string, line QueryLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendStepLine(chatID string, line StepLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendEventLine(chatID string, line EventLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendSubmitLine(chatID string, line SubmitLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

// chatJSONLPath returns the path to {chatId}.jsonl (flat file, matching Java).
func (s *FileStore) chatJSONLPath(chatID string) string {
	return filepath.Join(s.root, chatID+".jsonl")
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadSummary(chatID)
}

func (s *FileStore) loadSummary(chatID string) (*Summary, error) {
	var sum Summary
	var usage UsageData
	var pendingAwaitingID, pendingRunID, pendingMode string
	var pendingCreatedAt int64
	err := s.db.QueryRow("SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_, USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_ FROM CHATS WHERE CHAT_ID_=?", chatID).
		Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if usage.TotalTokens > 0 {
		sum.Usage = &usage
	}
	applyDerivedReadState(&sum)
	sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
	return &sum, nil
}

func applyDerivedReadState(sum *Summary) {
	if sum == nil {
		return
	}
	sum.Read.IsRead = !RunIDAfter(sum.LastRunID, sum.Read.ReadRunID)
}

func pendingAwaitingFromRow(awaitingID string, runID string, mode string, createdAt int64) *PendingAwaiting {
	if strings.TrimSpace(awaitingID) == "" {
		return nil
	}
	return &PendingAwaiting{
		AwaitingID: awaitingID,
		RunID:      runID,
		Mode:       mode,
		CreatedAt:  createdAt,
	}
}

func (s *FileStore) SetPendingAwaiting(chatID string, pending PendingAwaiting) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE CHATS
		SET PENDING_AWAITING_ID_=?, PENDING_AWAITING_RUN_ID_=?, PENDING_AWAITING_MODE_=?, PENDING_AWAITING_CREATED_AT_=?
		WHERE CHAT_ID_=?`,
		pending.AwaitingID, pending.RunID, pending.Mode, pending.CreatedAt, chatID)
	return err
}

func (s *FileStore) ClearPendingAwaiting(chatID string, awaitingID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE CHATS
		SET PENDING_AWAITING_ID_='', PENDING_AWAITING_RUN_ID_='', PENDING_AWAITING_MODE_='', PENDING_AWAITING_CREATED_AT_=0
		WHERE CHAT_ID_=? AND PENDING_AWAITING_ID_=?`,
		chatID, awaitingID)
	return err
}

// LoadRawMessages loads conversation history from {chatId}.jsonl step lines,
// falling back to {chatId}/raw_messages.jsonl for old chats.
func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = 20
	}

	// Try loading from step lines in {chatId}.jsonl (Java-compatible path)
	messages := s.loadRawMessagesFromJSONL(chatID)
	if len(messages) == 0 {
		// Fallback to old raw_messages.jsonl
		var err error
		messages, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
		if err != nil || len(messages) == 0 {
			return nil, err
		}
	}

	// Group by runId, keep last K runs (sliding window)
	type runBucket struct {
		runID    string
		messages []map[string]any
	}
	var runs []*runBucket
	runIndex := map[string]*runBucket{}
	for _, msg := range messages {
		runID, _ := msg["runId"].(string)
		if runID == "" {
			bucket := &runBucket{messages: []map[string]any{msg}}
			runs = append(runs, bucket)
			continue
		}
		bucket, ok := runIndex[runID]
		if !ok {
			bucket = &runBucket{runID: runID}
			runIndex[runID] = bucket
			runs = append(runs, bucket)
		}
		bucket.messages = append(bucket.messages, msg)
	}
	if len(runs) > k {
		runs = runs[len(runs)-k:]
	}
	var result []map[string]any
	for _, bucket := range runs {
		result = append(result, bucket.messages...)
	}
	return result, nil
}

// loadRawMessagesFromJSONL extracts OpenAI-format messages from step lines.
func (s *FileStore) loadRawMessagesFromJSONL(chatID string) []map[string]any {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil
	}
	if !isNewFormat(lines) {
		return nil
	}

	var messages []map[string]any
	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case "query":
			query, _ := line["query"].(map[string]any)
			if query == nil {
				continue
			}
			msg := map[string]any{
				"runId":   runID,
				"role":    stringValue(query["role"]),
				"content": stringValue(query["message"]),
				"ts":      line["updatedAt"],
			}
			messages = append(messages, msg)

		case "step", "react", "plan-execute":
			if strings.TrimSpace(stringValue(line["taskSubAgentKey"])) != "" {
				continue
			}
			rawMsgs, _ := line["messages"].([]any)
			for _, raw := range rawMsgs {
				m, _ := raw.(map[string]any)
				if m == nil {
					continue
				}
				role, _ := m["role"].(string)
				msg := map[string]any{"runId": runID}
				for k, v := range m {
					msg[k] = v
				}
				// Flatten content parts to plain text for LLM context
				if role == "user" || role == "assistant" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
					if parts, ok := m["reasoning_content"].([]any); ok {
						msg["reasoning_content"] = extractTextFromContent(parts)
					}
				}
				if role == "tool" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
				}
				messages = append(messages, msg)
			}
			if approval, ok := line["approval"].(map[string]any); ok {
				if summary := stringValue(approval["summary"]); summary != "" {
					messages = append(messages, map[string]any{
						"runId":   runID,
						"role":    "user",
						"content": summary,
						"ts":      line["updatedAt"],
					})
				}
			}
		}
	}
	return messages
}

func (s *FileStore) OnRunCompleted(completion RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE CHATS SET LAST_RUN_ID_=?, LAST_RUN_CONTENT_=?, UPDATED_AT_=?,
		USAGE_PROMPT_TOKENS_=USAGE_PROMPT_TOKENS_+?, USAGE_COMPLETION_TOKENS_=USAGE_COMPLETION_TOKENS_+?, USAGE_TOTAL_TOKENS_=USAGE_TOTAL_TOKENS_+?
		WHERE CHAT_ID_=?`,
		completion.RunID, completion.AssistantText, completion.UpdatedAtMillis,
		completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.TotalTokens,
		completion.ChatID)
	return err
}

func (s *FileStore) ListChats(lastRunID string, agentKey string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := "SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, LAST_RUN_ID_, LAST_RUN_CONTENT_, READ_RUN_ID_, READ_AT_, USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, PENDING_AWAITING_ID_, PENDING_AWAITING_RUN_ID_, PENDING_AWAITING_MODE_, PENDING_AWAITING_CREATED_AT_ FROM CHATS WHERE 1=1"
	var args []any
	if agentKey != "" {
		query += " AND AGENT_KEY_=?"
		args = append(args, agentKey)
	}
	query += " ORDER BY UPDATED_AT_ DESC, CHAT_ID_ DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Summary
	for rows.Next() {
		var sum Summary
		var usage UsageData
		var pendingAwaitingID, pendingRunID, pendingMode string
		var pendingCreatedAt int64
		if err := rows.Scan(&sum.ChatID, &sum.ChatName, &sum.AgentKey, &sum.TeamID, &sum.CreatedAt, &sum.UpdatedAt, &sum.LastRunID, &sum.LastRunContent, &sum.Read.ReadRunID, &sum.Read.ReadAt, &usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &pendingAwaitingID, &pendingRunID, &pendingMode, &pendingCreatedAt); err != nil {
			return nil, err
		}
		if usage.TotalTokens > 0 {
			sum.Usage = &usage
		}
		applyDerivedReadState(&sum)
		sum.PendingAwaiting = pendingAwaitingFromRow(pendingAwaitingID, pendingRunID, pendingMode, pendingCreatedAt)
		if lastRunID != "" && !RunIDAfter(sum.LastRunID, lastRunID) {
			continue
		}
		items = append(items, sum)
	}
	return items, rows.Err()
}

func (s *FileStore) LoadChat(chatID string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return Detail{}, err
	}
	if sum == nil {
		return Detail{}, ErrChatNotFound
	}

	// Read {chatId}.jsonl (flat file, Java format). Fallback to {chatId}/events.jsonl (old Go format).
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return Detail{}, err
	}
	if len(lines) == 0 {
		lines, err = readJSONLines(filepath.Join(s.ChatDir(chatID), "events.jsonl"))
		if err != nil {
			return Detail{}, err
		}
	}

	// Load raw messages for includeRawMessages support
	rawMessages := s.loadRawMessagesFromJSONL(chatID)
	if len(rawMessages) == 0 {
		rawMessages, _ = readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
	}

	// Detect format: new format has _type field, old format has type field.
	if isNewFormat(lines) {
		return s.loadChatNewFormat(*sum, lines, rawMessages)
	}
	return s.loadChatLegacyFormat(*sum, lines, rawMessages)
}

func (s *FileStore) LoadRunTrace(chatID string, runID string) (RunTrace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return RunTrace{}, err
	}
	if sum == nil {
		return RunTrace{}, ErrChatNotFound
	}
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return RunTrace{}, err
	}
	trace := RunTrace{
		ChatID:   chatID,
		ChatName: sum.ChatName,
		AgentKey: sum.AgentKey,
		TeamID:   sum.TeamID,
		RunID:    runID,
	}
	for _, line := range lines {
		lineRunID, _ := line["runId"].(string)
		if strings.TrimSpace(lineRunID) != strings.TrimSpace(runID) {
			continue
		}
		lineType, _ := line["_type"].(string)
		switch lineType {
		case "query":
			data, _ := json.Marshal(line)
			var query QueryLine
			if err := json.Unmarshal(data, &query); err == nil {
				trace.Query = &query
			}
		case "react", "plan-execute", "step":
			data, _ := json.Marshal(line)
			var step StepLine
			if err := json.Unmarshal(data, &step); err == nil {
				trace.Steps = append(trace.Steps, step)
				for _, message := range step.Messages {
					if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
						text := extractStoredMessageText(message)
						if strings.TrimSpace(text) != "" {
							trace.AssistantText = text
						}
					}
				}
			}
		}
	}
	if trace.Query == nil && len(trace.Steps) == 0 {
		return RunTrace{}, ErrChatNotFound
	}
	if strings.TrimSpace(trace.AssistantText) == "" {
		trace.AssistantText = sum.LastRunContent
	}
	return trace, nil
}

func extractStoredMessageText(message StoredMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// ---------------------------------------------------------------------------
// New format: _type = "query" / "step" / "event" (matching Java)
// ---------------------------------------------------------------------------

func (s *FileStore) loadChatNewFormat(summary Summary, lines []map[string]any, rawMessages []map[string]any) (Detail, error) {
	var plan *PlanState
	var artifact *ArtifactState

	runs := map[string]*chatRunData{}
	var runOrder []string
	var chatStartEvent *stream.EventData

	seq := int64(0)
	nextSeq := func() int64 { seq++; return seq }

	var chatTotalPromptTokens, chatTotalCompletionTokens, chatTotalTotalTokens int

	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		chatID, _ := line["chatId"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case "query":
			query, _ := line["query"].(map[string]any)
			if query == nil {
				query = map[string]any{}
			}
			payload := map[string]any{}
			for k, v := range query {
				payload[k] = v
			}
			if _, ok := payload["chatId"]; !ok {
				payload["chatId"] = chatID
			}

			rd := ensureRun(runs, &runOrder, runID)
			rd.events = append(rd.events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "request.query",
				Timestamp: int64FromAny(line["updatedAt"]),
				Payload:   payload,
			})

		case "react", "plan-execute", "step":
			rd := ensureRun(runs, &runOrder, runID)

			if rawPlan, ok := line["plan"].(map[string]any); ok {
				plan = parsePlanFromStep(rawPlan)
			}
			if rawArt, ok := line["artifacts"].(map[string]any); ok {
				artifact = parseArtifactFromStep(rawArt)
			}

			// new format uses "stage", legacy uses "_stage"
			stage, _ := line["stage"].(string)
			if stage == "" {
				stage, _ = line["_stage"].(string)
			}
			taskID, _ := line["taskId"].(string)
			taskGroupID, _ := line["taskGroupId"].(string)
			taskName, _ := line["taskName"].(string)
			taskDescription, _ := line["taskDescription"].(string)
			taskStatus, _ := line["taskStatus"].(string)
			taskSubAgentKey, _ := line["taskSubAgentKey"].(string)
			taskMainToolID, _ := line["taskMainToolId"].(string)
			if events := reconcileReplayedSubTask(rd, runID, taskID, taskGroupID, taskName, taskDescription, taskStatus, taskSubAgentKey, taskMainToolID, int64FromAny(line["updatedAt"]), nextSeq); len(events) > 0 {
				rd.events = append(rd.events, events...)
			}
			msgs, _ := line["messages"].([]any)
			awaitingReplay := newStepAwaitingReplay(line["awaiting"], runID)
			stepUsage, _ := line["usage"].(map[string]any)
			stepContextWindow, _ := line["contextWindow"].(map[string]any)
			stepSystem, _ := line["system"].(map[string]any)
			stepPreCallData := debugPreCallDataFromStepSystem(stepSystem)
			ts := int64FromAny(line["updatedAt"])
			if stepUsage != nil || len(stepContextWindow) > 0 || len(stepPreCallData) > 0 {
				runCumulativePre := map[string]int{
					"promptTokens":     rd.totalPromptTokens,
					"completionTokens": rd.totalCompletionTokens,
					"totalTokens":      rd.totalTotalTokens,
				}
				chatCumulativePre := map[string]int{
					"promptTokens":     chatTotalPromptTokens,
					"completionTokens": chatTotalCompletionTokens,
					"totalTokens":      chatTotalTotalTokens,
				}
				if ev := synthesizePreCallEvent(runID, chatID, runCumulativePre, chatCumulativePre, stepContextWindow, stepPreCallData, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
			}
			for _, rawMsg := range msgs {
				msgMap, _ := rawMsg.(map[string]any)
				if msgMap == nil {
					continue
				}
				for _, ev := range storedMessageToEvents(msgMap, runID, taskID, stage, nextSeq) {
					rd.events = append(rd.events, ev)
					if ev.Type == "tool.snapshot" {
						rd.events = append(rd.events, awaitingReplay.consumeForTool(ev.String("toolId"))...)
					}
				}
			}
			rd.events = append(rd.events, awaitingReplay.leftoverEvents()...)
			if stepUsage != nil {
				rd.totalPromptTokens += toIntValue(stepUsage["prompt_tokens"])
				rd.totalCompletionTokens += toIntValue(stepUsage["completion_tokens"])
				rd.totalTotalTokens += toIntValue(stepUsage["total_tokens"])
				chatTotalPromptTokens += toIntValue(stepUsage["prompt_tokens"])
				chatTotalCompletionTokens += toIntValue(stepUsage["completion_tokens"])
				chatTotalTotalTokens += toIntValue(stepUsage["total_tokens"])
			}
			if stepUsage != nil || len(stepContextWindow) > 0 {
				runCumulativePost := map[string]int{
					"promptTokens":     rd.totalPromptTokens,
					"completionTokens": rd.totalCompletionTokens,
					"totalTokens":      rd.totalTotalTokens,
				}
				chatCumulativePost := map[string]int{
					"promptTokens":     chatTotalPromptTokens,
					"completionTokens": chatTotalCompletionTokens,
					"totalTokens":      chatTotalTotalTokens,
				}
				if ev := synthesizePostCallEvent(runID, chatID, stepUsage, runCumulativePost, chatCumulativePost, stepContextWindow, ts, nextSeq); ev != nil {
					rd.events = append(rd.events, *ev)
				}
			}
		case "submit":
			rd := ensureRun(runs, &runOrder, runID)
			submit, _ := line["submit"].(map[string]any)
			answer, _ := line["answer"].(map[string]any)
			if len(submit) > 0 {
				if _, ok := submit["runId"]; !ok && runID != "" {
					submit["runId"] = runID
				}
				rd.events = append(rd.events, stream.EventDataFromMap(submit))
			}
			if len(answer) > 0 {
				if _, ok := answer["runId"]; !ok && runID != "" {
					answer["runId"] = runID
				}
				rd.events = append(rd.events, stream.EventDataFromMap(answer))
			}
		case "event", "steer":
			event, _ := line["event"].(map[string]any)
			if len(event) == 0 {
				continue
			}
			if _, ok := event["runId"]; !ok && runID != "" {
				event["runId"] = runID
			}
			rd := ensureRun(runs, &runOrder, runID)
			rd.events = append(rd.events, stream.EventDataFromMap(event))
		}
	}

	allEvents := make([]stream.EventData, 0)

	if chatStartEvent == nil && summary.ChatName != "" {
		allEvents = append(allEvents, stream.EventData{
			Seq:       nextSeq(),
			Type:      "chat.start",
			Timestamp: summary.CreatedAt,
			Payload:   map[string]any{"chatId": summary.ChatID, "chatName": summary.ChatName},
		})
	}

	for _, runID := range runOrder {
		rd := runs[runID]
		if events := flushReplayedSubTask(rd, nextSeq); len(events) > 0 {
			rd.events = append(rd.events, events...)
		}
		hasRunStart := false
		runStartTimestamp := int64(0)
		runCompleteTimestamp := int64(0)
		if len(rd.events) > 0 {
			runStartTimestamp = rd.events[0].Timestamp
			runCompleteTimestamp = rd.events[len(rd.events)-1].Timestamp
		}
		for _, ev := range rd.events {
			if ev.Type == "run.start" {
				hasRunStart = true
				break
			}
		}
		if !hasRunStart && runID != "" {
			allEvents = append(allEvents, stream.EventData{
				Seq:       nextSeq(),
				Type:      "run.start",
				Timestamp: runStartTimestamp,
				Payload:   map[string]any{"runId": runID, "chatId": summary.ChatID, "agentKey": summary.AgentKey},
			})
		}
		allEvents = append(allEvents, rd.events...)
		// Synthesize run.complete for the frontend (not persisted in JSONL).
		if runID != "" {
			payload := map[string]any{"runId": runID, "finishReason": "stop"}
			if rd.totalTotalTokens > 0 {
				payload["usage"] = map[string]any{
					"promptTokens":     rd.totalPromptTokens,
					"completionTokens": rd.totalCompletionTokens,
					"totalTokens":      rd.totalTotalTokens,
				}
			}
			allEvents = append(allEvents, stream.EventData{
				Seq:       nextSeq(),
				Type:      "run.complete",
				Timestamp: runCompleteTimestamp,
				Payload:   payload,
			})
		}
	}

	for i := range allEvents {
		allEvents[i].Seq = int64(i + 1)
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		RawMessages: rawMessages,
		Events:      allEvents,
		Plan:        plan,
		Artifact:    artifact,
	}, nil
}

// ---------------------------------------------------------------------------
// Legacy format: raw SSE events with "type" field (old Go format)
// ---------------------------------------------------------------------------

func (s *FileStore) loadChatLegacyFormat(summary Summary, events []map[string]any, rawMessages []map[string]any) (Detail, error) {
	events = rebuildSnapshotEvents(events)

	plan, artifact := deriveRunState(events)
	orderedEvents := make([]stream.EventData, 0, len(events))
	for _, event := range events {
		eventType, _ := event["type"].(string)
		if eventType == "plan.create" || eventType == "plan.update" || eventType == "artifact.publish" ||
			eventType == "stage.marker" {
			continue
		}
		orderedEvents = append(orderedEvents, stream.EventDataFromMap(event))
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		RawMessages: rawMessages,
		Events:      orderedEvents,
		Plan:        plan,
		Artifact:    artifact,
	}, nil
}

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

func isNewFormat(lines []map[string]any) bool {
	for _, line := range lines {
		if _, ok := line["_type"]; ok {
			return true
		}
		if _, ok := line["type"]; ok {
			return false
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Step line → SSE events reconstruction
// ---------------------------------------------------------------------------

func synthesizedContextWindow(contextWindow map[string]any) map[string]any {
	cw := map[string]any{}
	if len(contextWindow) == 0 {
		return cw
	}
	if v := toIntValue(contextWindow["max_size"]); v > 0 {
		cw["max_size"] = v
	}
	if v := toIntValue(contextWindow["actual_size"]); v > 0 {
		cw["actual_size"] = v
	}
	if v := toIntValue(contextWindow["estimated_size"]); v > 0 {
		cw["estimated_size"] = v
	}
	return cw
}

func cumulativeUsagePayload(cumulative map[string]int) map[string]any {
	if cumulative == nil {
		return map[string]any{"promptTokens": 0, "completionTokens": 0, "totalTokens": 0}
	}
	return map[string]any{
		"promptTokens":     cumulative["promptTokens"],
		"completionTokens": cumulative["completionTokens"],
		"totalTokens":      cumulative["totalTokens"],
	}
}

func synthesizePreCallEvent(runID, chatID string, runCumulative, chatCumulative map[string]int, contextWindow map[string]any, preCallData map[string]any, ts int64, nextSeq func() int64) *stream.EventData {
	data := cloneStringAnyMap(preCallData)
	if data == nil {
		data = map[string]any{}
	}
	if cw := synthesizedContextWindow(contextWindow); len(cw) > 0 {
		data["contextWindow"] = cw
	}
	data["usage"] = map[string]any{
		"runUsage":  cumulativeUsagePayload(runCumulative),
		"chatUsage": cumulativeUsagePayload(chatCumulative),
	}
	return &stream.EventData{
		Seq:       nextSeq(),
		Type:      "debug.preCall",
		Timestamp: ts,
		Payload: map[string]any{
			"runId":  runID,
			"chatId": chatID,
			"data":   data,
		},
	}
}

func debugPreCallDataFromStepSystem(system map[string]any) map[string]any {
	if len(system) == 0 {
		return nil
	}
	data, _ := system["debugPreCall"].(map[string]any)
	return cloneStringAnyMap(data)
}

func synthesizePostCallEvent(runID, chatID string, usage map[string]any, runCumulative, chatCumulative map[string]int, contextWindow map[string]any, ts int64, nextSeq func() int64) *stream.EventData {
	llm := map[string]any{"promptTokens": 0, "completionTokens": 0, "totalTokens": 0}
	if usage != nil {
		llm = map[string]any{
			"promptTokens":     toIntValue(usage["prompt_tokens"]),
			"completionTokens": toIntValue(usage["completion_tokens"]),
			"totalTokens":      toIntValue(usage["total_tokens"]),
		}
	}
	data := map[string]any{}
	if cw := synthesizedContextWindow(contextWindow); len(cw) > 0 {
		data["contextWindow"] = cw
	}
	data["usage"] = map[string]any{
		"llmReturnUsage": llm,
		"runUsage":       cumulativeUsagePayload(runCumulative),
		"chatUsage":      cumulativeUsagePayload(chatCumulative),
	}
	return &stream.EventData{
		Seq:       nextSeq(),
		Type:      "debug.postCall",
		Timestamp: ts,
		Payload: map[string]any{
			"runId":  runID,
			"chatId": chatID,
			"data":   data,
		},
	}
}

func toIntValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

type stepAwaitingReplay struct {
	items          []map[string]any
	awaitingByTool map[string][]int
	consumed       map[int]bool
}

func newStepAwaitingReplay(rawAwaiting any, runID string) *stepAwaitingReplay {
	awaitingList, _ := rawAwaiting.([]any)
	replay := &stepAwaitingReplay{
		items:          make([]map[string]any, 0, len(awaitingList)),
		awaitingByTool: map[string][]int{},
		consumed:       map[int]bool{},
	}
	for _, rawItem := range awaitingList {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}

		normalized := cloneStringAnyMap(item)
		if _, ok := normalized["runId"]; !ok && runID != "" {
			normalized["runId"] = runID
		}

		idx := len(replay.items)
		replay.items = append(replay.items, normalized)

		itemType, _ := normalized["type"].(string)
		if itemType != "awaiting.ask" {
			continue
		}
		awaitingID, _ := normalized["awaitingId"].(string)
		if awaitingID == "" {
			continue
		}
		replay.awaitingByTool[awaitingID] = append(replay.awaitingByTool[awaitingID], idx)
	}
	return replay
}

func (r *stepAwaitingReplay) consumeForTool(toolID string) []stream.EventData {
	if r == nil || toolID == "" {
		return nil
	}
	indexes := r.awaitingByTool[toolID]
	if len(indexes) == 0 {
		return nil
	}

	events := make([]stream.EventData, 0, len(indexes))
	for _, idx := range indexes {
		if r.consumed[idx] {
			continue
		}
		r.consumed[idx] = true
		events = append(events, stream.EventDataFromMap(r.items[idx]))
	}
	delete(r.awaitingByTool, toolID)
	return events
}

func (r *stepAwaitingReplay) leftoverEvents() []stream.EventData {
	if r == nil || len(r.items) == 0 {
		return nil
	}

	events := make([]stream.EventData, 0, len(r.items))
	for idx, item := range r.items {
		if r.consumed[idx] {
			continue
		}
		events = append(events, stream.EventDataFromMap(item))
	}
	return events
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func storedMessageToEvents(msg map[string]any, runID, taskID, stage string, nextSeq func() int64) []stream.EventData {
	role, _ := msg["role"].(string)
	ts := int64FromAny(msg["ts"])
	var events []stream.EventData

	switch role {
	case "assistant":
		if rc, ok := msg["reasoning_content"]; ok {
			text := extractTextFromContent(rc)
			if text != "" {
				reasoningID, _ := msg["_reasoningId"].(string)
				events = append(events, stream.EventData{
					Seq:       nextSeq(),
					Type:      "reasoning.snapshot",
					Timestamp: ts,
					Payload: map[string]any{
						"reasoningId":    reasoningID,
						"runId":          runID,
						"text":           text,
						"taskId":         taskID,
						"reasoningLabel": stream.ReasoningLabelForID(reasoningID),
					},
				})
			}
		}
		if c, ok := msg["content"]; ok {
			text := extractTextFromContent(c)
			if text != "" {
				contentID, _ := msg["_contentId"].(string)
				events = append(events, stream.EventData{
					Seq:       nextSeq(),
					Type:      "content.snapshot",
					Timestamp: ts,
					Payload: map[string]any{
						"contentId": contentID,
						"runId":     runID,
						"text":      text,
						"taskId":    taskID,
					},
				})
			}
		}
		if tcs, ok := msg["tool_calls"].([]any); ok {
			actionID, _ := msg["_actionId"].(string)
			toolID, _ := msg["_toolId"].(string)
			for _, tc := range tcs {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				fn, _ := tcMap["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
				}
				callID, _ := tcMap["id"].(string)
				fnName, _ := fn["name"].(string)
				fnArgs, _ := fn["arguments"].(string)

				if actionID != "" {
					events = append(events, stream.EventData{
						Seq:       nextSeq(),
						Type:      "action.snapshot",
						Timestamp: ts,
						Payload: map[string]any{
							"actionId":   callID,
							"runId":      runID,
							"actionName": fnName,
							"taskId":     taskID,
							"arguments":  fnArgs,
						},
					})
				} else {
					id := toolID
					if id == "" {
						id = callID
					}
					events = append(events, stream.EventData{
						Seq:       nextSeq(),
						Type:      "tool.snapshot",
						Timestamp: ts,
						Payload: map[string]any{
							"toolId":    id,
							"runId":     runID,
							"toolName":  fnName,
							"taskId":    taskID,
							"arguments": fnArgs,
						},
					})
				}
			}
		}

	case "tool":
		text := extractTextFromContent(msg["content"])
		actionID, _ := msg["_actionId"].(string)
		toolID, _ := msg["_toolId"].(string)
		toolCallID, _ := msg["tool_call_id"].(string)

		if actionID != "" {
			events = append(events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "action.result",
				Timestamp: ts,
				Payload: map[string]any{
					"actionId": toolCallID,
					"result":   text,
				},
			})
		} else {
			id := toolID
			if id == "" {
				id = toolCallID
			}
			events = append(events, stream.EventData{
				Seq:       nextSeq(),
				Type:      "tool.result",
				Timestamp: ts,
				Payload: map[string]any{
					"toolId": id,
					"result": text,
				},
			})
		}
	}

	return events
}

func extractTextFromContent(v any) string {
	if parts, ok := v.([]any); ok {
		var sb strings.Builder
		for _, part := range parts {
			if pMap, ok := part.(map[string]any); ok {
				if text, ok := pMap["text"].(string); ok {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	}
	if text, ok := v.(string); ok {
		return text
	}
	return ""
}

func parsePlanFromStep(raw map[string]any) *PlanState {
	planID, _ := raw["planId"].(string)
	plan := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}
	tasks, _ := raw["tasks"].([]any)
	for _, t := range tasks {
		tMap, _ := t.(map[string]any)
		if tMap == nil {
			continue
		}
		plan.Tasks = append(plan.Tasks, PlanTaskState{
			TaskID:      stringValue(tMap["taskId"]),
			Description: stringValue(tMap["description"]),
			Status:      stringValue(tMap["status"]),
		})
	}
	return plan
}

func parseArtifactFromStep(raw map[string]any) *ArtifactState {
	return &ArtifactState{Items: artifactItemsFromValue(raw["items"])}
}

type chatRunData struct {
	runID                 string
	agentKey              string
	events                []stream.EventData
	totalPromptTokens     int
	totalCompletionTokens int
	totalTotalTokens      int
	activeSubTasks        map[string]*replayedSubTask
}

type replayedSubTask struct {
	TaskID        string
	GroupID       string
	TaskName      string
	TaskDesc      string
	SubAgentKey   string
	MainToolID    string
	Status        string
	LastTimestamp int64
}

func ensureRun(runs map[string]*chatRunData, order *[]string, runID string) *chatRunData {
	if rd, ok := runs[runID]; ok {
		return rd
	}
	rd := &chatRunData{runID: runID}
	runs[runID] = rd
	*order = append(*order, runID)
	return rd
}

func reconcileReplayedSubTask(rd *chatRunData, runID string, taskID string, taskGroupID string, taskName string, taskDescription string, taskStatus string, taskSubAgentKey string, taskMainToolID string, ts int64, nextSeq func() int64) []stream.EventData {
	if rd == nil {
		return nil
	}
	var events []stream.EventData
	isCurrentSubTask := strings.TrimSpace(taskID) != "" && strings.TrimSpace(taskSubAgentKey) != ""
	if !isCurrentSubTask {
		return nil
	}
	if rd.activeSubTasks == nil {
		rd.activeSubTasks = map[string]*replayedSubTask{}
	}
	active := rd.activeSubTasks[taskID]
	if active == nil {
		active = &replayedSubTask{
			TaskID:        taskID,
			GroupID:       taskGroupID,
			TaskName:      taskName,
			TaskDesc:      taskDescription,
			SubAgentKey:   taskSubAgentKey,
			MainToolID:    taskMainToolID,
			Status:        taskStatus,
			LastTimestamp: ts,
		}
		rd.activeSubTasks[taskID] = active
		events = append(events, stream.EventData{
			Seq:       nextSeq(),
			Type:      "task.start",
			Timestamp: ts,
			Payload: map[string]any{
				"taskId":      taskID,
				"runId":       runID,
				"groupId":     taskGroupID,
				"taskName":    taskName,
				"description": taskDescription,
				"subAgentKey": taskSubAgentKey,
				"mainToolId":  taskMainToolID,
			},
		})
	}
	if strings.TrimSpace(taskGroupID) != "" {
		active.GroupID = taskGroupID
	}
	if strings.TrimSpace(taskName) != "" {
		active.TaskName = taskName
	}
	if strings.TrimSpace(taskDescription) != "" {
		active.TaskDesc = taskDescription
	}
	if strings.TrimSpace(taskMainToolID) != "" {
		active.MainToolID = taskMainToolID
	}
	if strings.TrimSpace(taskStatus) != "" {
		active.Status = taskStatus
	}
	active.LastTimestamp = ts
	if isTerminalSubTaskStatus(active.Status) {
		events = append(events, synthesizeReplayedSubTaskTerminal(runID, active, nextSeq)...)
		delete(rd.activeSubTasks, taskID)
	}
	return events
}

func flushReplayedSubTask(rd *chatRunData, nextSeq func() int64) []stream.EventData {
	if rd == nil || len(rd.activeSubTasks) == 0 {
		return nil
	}
	taskIDs := make([]string, 0, len(rd.activeSubTasks))
	for taskID := range rd.activeSubTasks {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	events := make([]stream.EventData, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		events = append(events, synthesizeReplayedSubTaskTerminal(rd.runID, rd.activeSubTasks[taskID], nextSeq)...)
		delete(rd.activeSubTasks, taskID)
	}
	return events
}

func synthesizeReplayedSubTaskTerminal(runID string, task *replayedSubTask, nextSeq func() int64) []stream.EventData {
	if task == nil {
		return nil
	}
	status := strings.TrimSpace(task.Status)
	if status == "" {
		status = "completed"
	}
	switch status {
	case "cancelled":
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.cancel",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId":  task.TaskID,
				"groupId": task.GroupID,
				"status":  "cancelled",
			},
		}}
	case "error":
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.fail",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId":  task.TaskID,
				"groupId": task.GroupID,
				"status":  "error",
				"error": map[string]any{
					"code":     "sub_agent_failed",
					"message":  "sub-agent failed",
					"scope":    "task",
					"category": "system",
				},
			},
		}}
	default:
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.complete",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId":  task.TaskID,
				"groupId": task.GroupID,
				"status":  "completed",
			},
		}}
	}
}

func isTerminalSubTaskStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "complete", "cancelled", "canceled", "error", "failed", "fail":
		return true
	default:
		return false
	}
}

func int64FromAny(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// Legacy format helpers
// ---------------------------------------------------------------------------

func deriveRunState(events []map[string]any) (*PlanState, *ArtifactState) {
	var plan *PlanState
	var artifact *ArtifactState
	for _, event := range events {
		eventType, _ := event["type"].(string)
		switch eventType {
		case "plan.create", "plan.update":
			planID, _ := event["planId"].(string)
			next := &PlanState{PlanID: planID, Tasks: []PlanTaskState{}}
			rawPlan := event["plan"]
			if items, ok := rawPlan.([]any); ok {
				for _, item := range items {
					mapped, _ := item.(map[string]any)
					if mapped == nil {
						continue
					}
					next.Tasks = append(next.Tasks, PlanTaskState{
						TaskID:      stringValue(mapped["taskId"]),
						Description: stringValue(mapped["description"]),
						Status:      stringValue(mapped["status"]),
					})
				}
				plan = next
				continue
			}
			if rawMap, ok := rawPlan.(map[string]any); ok {
				var rawTasks any
				rawTasks = rawMap["tasks"]
				if rawTasks == nil {
					rawTasks = rawMap["plan"]
				}
				if items, ok := rawTasks.([]any); ok {
					for _, item := range items {
						mapped, _ := item.(map[string]any)
						if mapped == nil {
							continue
						}
						next.Tasks = append(next.Tasks, PlanTaskState{
							TaskID:      stringValue(mapped["taskId"]),
							Description: stringValue(mapped["description"]),
							Status:      stringValue(mapped["status"]),
						})
					}
				}
			}
			plan = next
		case "artifact.publish":
			if artifact == nil {
				artifact = &ArtifactState{}
			}
			artifact.Items = append(artifact.Items, artifactItemsFromEventPayload(event)...)
		}
	}
	return plan, artifact
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func (s *FileStore) MarkRead(chatID string, runID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum, err := s.loadSummary(chatID)
	if err != nil {
		return Summary{}, err
	}
	if sum == nil {
		return Summary{}, ErrChatNotFound
	}

	nextReadRunID := strings.TrimSpace(runID)
	if nextReadRunID == "" {
		nextReadRunID = sum.LastRunID
	}
	if sum.LastRunID != "" && RunIDAfter(nextReadRunID, sum.LastRunID) {
		nextReadRunID = sum.LastRunID
	}
	if !RunIDAfter(nextReadRunID, sum.Read.ReadRunID) {
		nextReadRunID = sum.Read.ReadRunID
	}

	now := time.Now().UnixMilli()
	result, err := s.db.Exec("UPDATE CHATS SET READ_RUN_ID_=?, READ_AT_=? WHERE CHAT_ID_=?", nextReadRunID, now, chatID)
	if err != nil {
		return Summary{}, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return Summary{}, ErrChatNotFound
	}
	sum, err = s.loadSummary(chatID)
	if err != nil || sum == nil {
		return Summary{}, ErrChatNotFound
	}
	return *sum, nil
}

func (s *FileStore) AgentChatStats() (map[string]AgentChatStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT AGENT_KEY_, LAST_RUN_ID_, READ_RUN_ID_ FROM CHATS`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := map[string]AgentChatStats{}
	for rows.Next() {
		var agentKey, lastRunID, readRunID string
		if err := rows.Scan(&agentKey, &lastRunID, &readRunID); err != nil {
			return nil, err
		}
		item := stats[agentKey]
		item.TotalCount++
		if lastRunID != "" && RunIDAfter(lastRunID, readRunID) {
			item.UnreadCount++
		}
		stats[agentKey] = item
	}
	return stats, rows.Err()
}

func (s *FileStore) ResolveResource(file string) (string, error) {
	clean := filepath.Clean(file)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", os.ErrPermission
	}
	path := filepath.Join(s.root, clean)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *FileStore) ChatDir(chatID string) string {
	return filepath.Join(s.root, chatID)
}

func (s *FileStore) appendJSONLine(path string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	return encoder.Encode(payload)
}

// readJSONLines reads a JSONL file. Uses json.Decoder so it handles both
// single-line JSON objects (Go's writer) and pretty-printed multi-line JSON
// objects (Java may write either format).
func readJSONLines(path string) ([]map[string]any, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var items []map[string]any
	decoder := json.NewDecoder(file)
	for {
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse JSONL: %w", err)
		}
		if payload != nil {
			items = append(items, payload)
		}
	}
	return items, nil
}

func defaultChatName(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "default"
	}
	runes := []rune(message)
	if len(runes) > 24 {
		return string(runes[:24])
	}
	return message
}

// RunIDAfter and related helpers are in run_id.go
