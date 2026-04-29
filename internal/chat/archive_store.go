package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type ArchiveStore struct {
	root string
	mu   sync.Mutex
	db   *sql.DB
}

func NewArchiveStore(chatsRoot string) (*ArchiveStore, error) {
	root := filepath.Join(chatsRoot, "archive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	store := &ArchiveStore{root: root}
	if err := store.initDB(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ArchiveStore) initDB() error {
	db, err := sql.Open("sqlite", filepath.Join(s.root, "archive.db"))
	if err != nil {
		return fmt.Errorf("open archive.db: %w", err)
	}
	s.db = db

	statements := []string{
		`CREATE TABLE IF NOT EXISTS ARCHIVED_CHATS (
			CHAT_ID_          TEXT PRIMARY KEY,
			CHAT_NAME_        TEXT NOT NULL,
			AGENT_KEY_        TEXT NOT NULL DEFAULT '',
			TEAM_ID_          TEXT,
			CREATED_AT_       INTEGER NOT NULL,
			UPDATED_AT_       INTEGER NOT NULL,
			ARCHIVED_AT_      INTEGER NOT NULL,
			LAST_RUN_ID_      TEXT NOT NULL DEFAULT '',
			LAST_RUN_CONTENT_ TEXT NOT NULL DEFAULT '',
			USAGE_PROMPT_TOKENS_     INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_      INTEGER NOT NULL DEFAULT 0,
			JSONL_CONTENT_           TEXT NOT NULL DEFAULT '',
			EVENTS_CONTENT_          TEXT NOT NULL DEFAULT '',
			RAW_MESSAGES_CONTENT_    TEXT NOT NULL DEFAULT '',
			HAS_ATTACHMENTS_         INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS IDX_ARCHIVED_CHATS_AGENT_KEY_ ON ARCHIVED_CHATS(AGENT_KEY_)`,
		`CREATE INDEX IF NOT EXISTS IDX_ARCHIVED_CHATS_ARCHIVED_AT_ ON ARCHIVED_CHATS(ARCHIVED_AT_)`,
		`CREATE TABLE IF NOT EXISTS ARCHIVED_RUNS (
			RUN_ID_            TEXT PRIMARY KEY,
			CHAT_ID_           TEXT NOT NULL,
			AGENT_KEY_         TEXT NOT NULL DEFAULT '',
			INITIAL_MESSAGE_   TEXT NOT NULL DEFAULT '',
			ASSISTANT_TEXT_    TEXT NOT NULL DEFAULT '',
			FINISH_REASON_     TEXT NOT NULL DEFAULT '',
			STARTED_AT_        INTEGER NOT NULL DEFAULT 0,
			COMPLETED_AT_      INTEGER NOT NULL,
			USAGE_PROMPT_TOKENS_     INTEGER NOT NULL DEFAULT 0,
			USAGE_COMPLETION_TOKENS_ INTEGER NOT NULL DEFAULT 0,
			USAGE_TOTAL_TOKENS_      INTEGER NOT NULL DEFAULT 0,
			FEEDBACK_TYPE_     TEXT NOT NULL DEFAULT '',
			FEEDBACK_COMMENT_  TEXT NOT NULL DEFAULT '',
			FEEDBACK_AT_       INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS IDX_ARCHIVED_RUNS_CHAT_ID_ ON ARCHIVED_RUNS(CHAT_ID_)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS ARCHIVED_CHATS_FTS USING fts5(
			CHAT_NAME_, LAST_RUN_CONTENT_,
			content=ARCHIVED_CHATS, content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS ARCHIVED_CHATS_AI AFTER INSERT ON ARCHIVED_CHATS BEGIN
			INSERT INTO ARCHIVED_CHATS_FTS(rowid, CHAT_NAME_, LAST_RUN_CONTENT_)
			VALUES (new.rowid, new.CHAT_NAME_, new.LAST_RUN_CONTENT_);
		END`,
		`CREATE TRIGGER IF NOT EXISTS ARCHIVED_CHATS_AD AFTER DELETE ON ARCHIVED_CHATS BEGIN
			INSERT INTO ARCHIVED_CHATS_FTS(ARCHIVED_CHATS_FTS, rowid, CHAT_NAME_, LAST_RUN_CONTENT_)
			VALUES ('delete', old.rowid, old.CHAT_NAME_, old.LAST_RUN_CONTENT_);
		END`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("init archive schema: %w", err)
		}
	}
	return nil
}

func (s *ArchiveStore) ArchiveChat(chat ArchivedChat) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chat.Summary.ChatID = strings.TrimSpace(chat.Summary.ChatID)
	if !ValidChatID(chat.Summary.ChatID) {
		return os.ErrPermission
	}
	exists, err := s.existsLocked(chat.Summary.ChatID)
	if err != nil {
		return err
	}
	if exists {
		return ErrChatAlreadyArchived
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	usage := UsageData{}
	if chat.Summary.Usage != nil {
		usage = *chat.Summary.Usage
	}
	hasAttachments := 0
	if chat.Summary.HasAttachments {
		hasAttachments = 1
	}
	if chat.Summary.ArchivedAt <= 0 {
		chat.Summary.ArchivedAt = time.Now().UnixMilli()
	}
	_, err = tx.Exec(`INSERT INTO ARCHIVED_CHATS (
			CHAT_ID_, CHAT_NAME_, AGENT_KEY_, TEAM_ID_, CREATED_AT_, UPDATED_AT_, ARCHIVED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
			JSONL_CONTENT_, EVENTS_CONTENT_, RAW_MESSAGES_CONTENT_, HAS_ATTACHMENTS_
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chat.Summary.ChatID, chat.Summary.ChatName, chat.Summary.AgentKey, nilIfEmpty(chat.Summary.TeamID),
		chat.Summary.CreatedAt, chat.Summary.UpdatedAt, chat.Summary.ArchivedAt,
		chat.Summary.LastRunID, chat.Summary.LastRunContent,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		chat.JSONLContent, chat.EventsContent, chat.RawMessagesContent, hasAttachments)
	if err != nil {
		return err
	}
	for _, run := range chat.Runs {
		_, err = tx.Exec(`INSERT INTO ARCHIVED_RUNS (
				RUN_ID_, CHAT_ID_, AGENT_KEY_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
				STARTED_AT_, COMPLETED_AT_,
				USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
				FEEDBACK_TYPE_, FEEDBACK_COMMENT_, FEEDBACK_AT_
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.RunID, run.ChatID, run.AgentKey, run.InitialMessage, run.AssistantText, run.FinishReason,
			run.StartedAt, run.CompletedAt,
			run.Usage.PromptTokens, run.Usage.CompletionTokens, run.Usage.TotalTokens,
			run.FeedbackType, run.FeedbackComment, run.FeedbackAt)
		if err != nil {
			return err
		}
	}
	err = tx.Commit()
	return err
}

func (s *ArchiveStore) ListArchived(agentKey string, limit, offset int) ([]ArchivedSummary, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	where := "WHERE 1=1"
	var args []any
	if strings.TrimSpace(agentKey) != "" {
		where += " AND AGENT_KEY_=?"
		args = append(args, strings.TrimSpace(agentKey))
	}
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM ARCHIVED_CHATS "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append(append([]any(nil), args...), limit, offset)
	rows, err := s.db.Query(`SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, ARCHIVED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_, HAS_ATTACHMENTS_
		FROM ARCHIVED_CHATS `+where+`
		ORDER BY ARCHIVED_AT_ DESC, UPDATED_AT_ DESC, CHAT_ID_ DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := scanArchivedSummaries(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, rows.Err()
}

func (s *ArchiveStore) LoadArchived(chatID string) (*ArchivedChat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return nil, os.ErrPermission
	}
	row := s.db.QueryRow(`SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), CREATED_AT_, UPDATED_AT_, ARCHIVED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_,
			USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
			JSONL_CONTENT_, EVENTS_CONTENT_, RAW_MESSAGES_CONTENT_, HAS_ATTACHMENTS_
		FROM ARCHIVED_CHATS WHERE CHAT_ID_=?`, chatID)
	archived, err := scanArchivedChatRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrChatNotFound
	}
	if err != nil {
		return nil, err
	}
	runs, err := s.listRunsLocked(chatID)
	if err != nil {
		return nil, err
	}
	archived.Runs = runs

	lines, err := readJSONLinesContent(archived.JSONLContent)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		lines, err = readJSONLinesContent(archived.EventsContent)
		if err != nil {
			return nil, err
		}
	}
	rawMessages, err := readJSONLinesContent(archived.RawMessagesContent)
	if err != nil {
		return nil, err
	}
	if len(rawMessages) == 0 {
		rawMessages = rawMessagesFromJSONLLines(lines)
	}
	summary := Summary{
		ChatID:         archived.Summary.ChatID,
		ChatName:       archived.Summary.ChatName,
		AgentKey:       archived.Summary.AgentKey,
		TeamID:         archived.Summary.TeamID,
		CreatedAt:      archived.Summary.CreatedAt,
		UpdatedAt:      archived.Summary.UpdatedAt,
		LastRunID:      archived.Summary.LastRunID,
		LastRunContent: archived.Summary.LastRunContent,
		Usage:          archived.Summary.Usage,
	}
	if isNewFormat(lines) {
		archived.Detail, err = parseChatNewFormat(summary, lines, rawMessages)
	} else {
		archived.Detail, err = parseChatLegacyFormat(summary, lines, rawMessages)
	}
	if err != nil {
		return nil, err
	}
	return archived, nil
}

func (s *ArchiveStore) SearchArchived(query, agentKey string, limit int) ([]ArchiveSearchHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	hits, err := s.searchArchivedFTSLocked(query, agentKey, limit)
	if err == nil {
		return hits, nil
	}
	return s.searchArchivedLikeLocked(query, agentKey, limit)
}

func (s *ArchiveStore) DeleteArchived(chatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return os.ErrPermission
	}
	result, err := s.db.Exec("DELETE FROM ARCHIVED_CHATS WHERE CHAT_ID_=?", chatID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrChatNotFound
	}
	if _, err := s.db.Exec("DELETE FROM ARCHIVED_RUNS WHERE CHAT_ID_=?", chatID); err != nil {
		return err
	}
	if err := os.RemoveAll(s.ChatDir(chatID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *ArchiveStore) ResolveResource(chatID, file string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return "", os.ErrPermission
	}
	clean := filepath.Clean(strings.TrimSpace(file))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", os.ErrPermission
	}
	path := filepath.Join(s.ChatDir(chatID), clean)
	rel, err := filepath.Rel(s.ChatDir(chatID), path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", os.ErrPermission
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *ArchiveStore) ChatDir(chatID string) string {
	return filepath.Join(s.root, chatID)
}

func (s *ArchiveStore) existsLocked(chatID string) (bool, error) {
	var exists int
	err := s.db.QueryRow("SELECT 1 FROM ARCHIVED_CHATS WHERE CHAT_ID_=?", chatID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *ArchiveStore) listRunsLocked(chatID string) ([]RunSummary, error) {
	rows, err := s.db.Query(`SELECT RUN_ID_, CHAT_ID_, AGENT_KEY_, INITIAL_MESSAGE_, ASSISTANT_TEXT_, FINISH_REASON_,
		STARTED_AT_, COMPLETED_AT_,
		USAGE_PROMPT_TOKENS_, USAGE_COMPLETION_TOKENS_, USAGE_TOTAL_TOKENS_,
		FEEDBACK_TYPE_, FEEDBACK_COMMENT_, FEEDBACK_AT_
		FROM ARCHIVED_RUNS WHERE CHAT_ID_=? ORDER BY COMPLETED_AT_ DESC, RUN_ID_ DESC`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RunSummary
	for rows.Next() {
		var item RunSummary
		if err := rows.Scan(
			&item.RunID, &item.ChatID, &item.AgentKey, &item.InitialMessage, &item.AssistantText, &item.FinishReason,
			&item.StartedAt, &item.CompletedAt,
			&item.Usage.PromptTokens, &item.Usage.CompletionTokens, &item.Usage.TotalTokens,
			&item.FeedbackType, &item.FeedbackComment, &item.FeedbackAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ArchiveStore) searchArchivedFTSLocked(query, agentKey string, limit int) ([]ArchiveSearchHit, error) {
	ftsQuery := archiveFTSQuery(query)
	rows, err := s.db.Query(`SELECT c.CHAT_ID_, c.CHAT_NAME_, c.AGENT_KEY_, COALESCE(c.TEAM_ID_,''), c.ARCHIVED_AT_,
			c.LAST_RUN_ID_, c.LAST_RUN_CONTENT_, bm25(ARCHIVED_CHATS_FTS)
		FROM ARCHIVED_CHATS_FTS
		JOIN ARCHIVED_CHATS c ON c.rowid=ARCHIVED_CHATS_FTS.rowid
		WHERE ARCHIVED_CHATS_FTS MATCH ? AND (?='' OR c.AGENT_KEY_=?)
		ORDER BY bm25(ARCHIVED_CHATS_FTS), c.ARCHIVED_AT_ DESC
		LIMIT ?`, ftsQuery, strings.TrimSpace(agentKey), strings.TrimSpace(agentKey), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []ArchiveSearchHit
	for rows.Next() {
		var hit ArchiveSearchHit
		var rank float64
		if err := rows.Scan(&hit.ChatID, &hit.ChatName, &hit.AgentKey, &hit.TeamID, &hit.ArchivedAt, &hit.LastRunID, &hit.LastRunContent, &rank); err != nil {
			return nil, err
		}
		hit.Snippet = buildArchiveSnippet(query, hit.ChatName, hit.LastRunContent)
		hit.Score = int(1000 - rank*100)
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func (s *ArchiveStore) searchArchivedLikeLocked(query, agentKey string, limit int) ([]ArchiveSearchHit, error) {
	like := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.Query(`SELECT CHAT_ID_, CHAT_NAME_, AGENT_KEY_, COALESCE(TEAM_ID_,''), ARCHIVED_AT_,
			LAST_RUN_ID_, LAST_RUN_CONTENT_
		FROM ARCHIVED_CHATS
		WHERE (?='' OR AGENT_KEY_=?) AND (
			lower(CHAT_NAME_) LIKE ? OR lower(LAST_RUN_CONTENT_) LIKE ? OR lower(JSONL_CONTENT_) LIKE ? OR lower(EVENTS_CONTENT_) LIKE ?
		)
		ORDER BY ARCHIVED_AT_ DESC, UPDATED_AT_ DESC
		LIMIT ?`, strings.TrimSpace(agentKey), strings.TrimSpace(agentKey), like, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []ArchiveSearchHit
	for rows.Next() {
		var hit ArchiveSearchHit
		if err := rows.Scan(&hit.ChatID, &hit.ChatName, &hit.AgentKey, &hit.TeamID, &hit.ArchivedAt, &hit.LastRunID, &hit.LastRunContent); err != nil {
			return nil, err
		}
		hit.Snippet = buildArchiveSnippet(query, hit.ChatName, hit.LastRunContent)
		hit.Score = sessionSearchScore(hit.ChatName+"\n"+hit.LastRunContent, strings.ToLower(query))
		if hit.Score == 0 {
			hit.Score = 1
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func archiveFTSQuery(query string) string {
	query = strings.TrimSpace(query)
	query = strings.ReplaceAll(query, `"`, `""`)
	return `"` + query + `"`
}

func buildArchiveSnippet(query, chatName, lastRunContent string) string {
	needle := strings.ToLower(strings.TrimSpace(query))
	if snippet := buildSnippet(chatName, needle); strings.TrimSpace(snippet) != "" {
		return snippet
	}
	if snippet := buildSnippet(lastRunContent, needle); strings.TrimSpace(snippet) != "" {
		return snippet
	}
	text := strings.TrimSpace(lastRunContent)
	if text == "" {
		text = strings.TrimSpace(chatName)
	}
	return truncateRunes(text, 200)
}

type archivedSummaryScanner interface {
	Scan(dest ...any) error
}

func scanArchivedChatRow(row archivedSummaryScanner) (*ArchivedChat, error) {
	var item ArchivedChat
	var usage UsageData
	var hasAttachments int
	if err := row.Scan(
		&item.Summary.ChatID, &item.Summary.ChatName, &item.Summary.AgentKey, &item.Summary.TeamID,
		&item.Summary.CreatedAt, &item.Summary.UpdatedAt, &item.Summary.ArchivedAt,
		&item.Summary.LastRunID, &item.Summary.LastRunContent,
		&usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens,
		&item.JSONLContent, &item.EventsContent, &item.RawMessagesContent, &hasAttachments,
	); err != nil {
		return nil, err
	}
	if usage.TotalTokens > 0 {
		item.Summary.Usage = &usage
	}
	item.Summary.HasAttachments = hasAttachments != 0
	return &item, nil
}

func scanArchivedSummaries(rows *sql.Rows) ([]ArchivedSummary, error) {
	var items []ArchivedSummary
	for rows.Next() {
		var item ArchivedSummary
		var usage UsageData
		var hasAttachments int
		if err := rows.Scan(
			&item.ChatID, &item.ChatName, &item.AgentKey, &item.TeamID,
			&item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt,
			&item.LastRunID, &item.LastRunContent,
			&usage.PromptTokens, &usage.CompletionTokens, &usage.TotalTokens, &hasAttachments,
		); err != nil {
			return nil, err
		}
		if usage.TotalTokens > 0 {
			item.Usage = &usage
		}
		item.HasAttachments = hasAttachments != 0
		items = append(items, item)
	}
	return items, nil
}

func readJSONLinesContent(content string) ([]map[string]any, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	var items []map[string]any
	for {
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse archived JSONL: %w", err)
		}
		if payload != nil {
			items = append(items, payload)
		}
	}
	return items, nil
}
