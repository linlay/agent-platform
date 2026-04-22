package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/skills"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	root            string
	dbPath          string
	dualWriteMD     bool
	mu              sync.Mutex
	db              *sql.DB
	ftsVectorWeight float64
	ftsFTSWeight    float64
	embedder        *EmbeddingProvider
	summarizer      RememberSummarizer
}

func NewSQLiteStore(root string, dbFileName string) (*SQLiteStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dbFileName) == "" {
		dbFileName = "memory.db"
	}
	store := &SQLiteStore{
		root:            root,
		dbPath:          filepath.Join(root, dbFileName),
		dualWriteMD:     true,
		ftsVectorWeight: 0.7,
		ftsFTSWeight:    0.3,
	}
	if err := store.initDB(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) SetEmbedder(ep *EmbeddingProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedder = ep
}

func (s *SQLiteStore) SetRememberSummarizer(summarizer RememberSummarizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summarizer = summarizer
}

func (s *SQLiteStore) ApplyFeedback(signals []FeedbackSignal) error {
	if len(signals) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	for _, sig := range signals {
		_, err := s.db.Exec(
			`UPDATE MEMORIES SET
				CONFIDENCE_ = MAX(0.1, MIN(1.0, CONFIDENCE_ + ?)),
				ACCESS_COUNT_ = ACCESS_COUNT_ + 1,
				LAST_ACCESSED_AT_ = ?,
				UPDATED_AT_ = ?
			WHERE ID_ = ?`,
			sig.ConfidenceDelta, now, now, sig.ItemID,
		)
		if err != nil {
			return fmt.Errorf("apply feedback for %s: %w", sig.ItemID, err)
		}
		_, _ = s.db.Exec(
			`UPDATE MEMORY_FACTS SET CONFIDENCE_ = MAX(0.1, MIN(1.0, CONFIDENCE_ + ?)), UPDATED_AT_ = ? WHERE ID_ = ?`,
			sig.ConfidenceDelta, now, sig.ItemID,
		)
		_, _ = s.db.Exec(
			`UPDATE MEMORY_OBSERVATIONS SET CONFIDENCE_ = MAX(0.1, MIN(1.0, CONFIDENCE_ + ?)), UPDATED_AT_ = ? WHERE ID_ = ?`,
			sig.ConfidenceDelta, now, sig.ItemID,
		)
	}
	logMemoryOperation("apply_feedback", map[string]any{"signalCount": len(signals)})
	return nil
}

func (s *SQLiteStore) initDB() error {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return err
	}
	s.db = db

	statements := []string{
		`CREATE TABLE IF NOT EXISTS MEMORIES (
			ID_ TEXT PRIMARY KEY,
			TS_ INTEGER NOT NULL,
			REQUEST_ID_ TEXT,
			CHAT_ID_ TEXT,
			AGENT_KEY_ TEXT NOT NULL DEFAULT '',
			SUBJECT_KEY_ TEXT NOT NULL DEFAULT '',
			SOURCE_TYPE_ TEXT NOT NULL DEFAULT '',
			SUMMARY_ TEXT NOT NULL DEFAULT '',
			CATEGORY_ TEXT DEFAULT 'general',
			IMPORTANCE_ INTEGER DEFAULT 5,
			TAGS_ TEXT,
			EMBEDDING_ BLOB,
			EMBEDDING_MODEL_ TEXT,
			UPDATED_AT_ INTEGER NOT NULL,
			ACCESS_COUNT_ INTEGER DEFAULT 0,
			LAST_ACCESSED_AT_ INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS MEMORY_FACTS (
			ID_ TEXT PRIMARY KEY,
			AGENT_KEY_ TEXT NOT NULL DEFAULT '',
			SCOPE_TYPE_ TEXT NOT NULL DEFAULT 'agent',
			SCOPE_KEY_ TEXT NOT NULL DEFAULT '',
			CATEGORY_ TEXT NOT NULL DEFAULT 'general',
			TITLE_ TEXT NOT NULL DEFAULT '',
			CONTENT_ TEXT NOT NULL DEFAULT '',
			TAGS_ TEXT NOT NULL DEFAULT '',
			IMPORTANCE_ INTEGER NOT NULL DEFAULT 5,
			CONFIDENCE_ REAL NOT NULL DEFAULT 0.9,
			STATUS_ TEXT NOT NULL DEFAULT 'active',
			SOURCE_KIND_ TEXT NOT NULL DEFAULT 'manual',
			SOURCE_REF_ TEXT NOT NULL DEFAULT '',
			DEDUPE_KEY_ TEXT NOT NULL DEFAULT '',
			CREATED_AT_ INTEGER NOT NULL,
			UPDATED_AT_ INTEGER NOT NULL,
			LAST_CONFIRMED_AT_ INTEGER,
			EXPIRES_AT_ INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS MEMORY_OBSERVATIONS (
			ID_ TEXT PRIMARY KEY,
			AGENT_KEY_ TEXT NOT NULL DEFAULT '',
			CHAT_ID_ TEXT NOT NULL DEFAULT '',
			RUN_ID_ TEXT NOT NULL DEFAULT '',
			SCOPE_TYPE_ TEXT NOT NULL DEFAULT 'chat',
			SCOPE_KEY_ TEXT NOT NULL DEFAULT '',
			TYPE_ TEXT NOT NULL DEFAULT 'discovery',
			TITLE_ TEXT NOT NULL DEFAULT '',
			SUMMARY_ TEXT NOT NULL DEFAULT '',
			DETAIL_ TEXT NOT NULL DEFAULT '',
			FILES_JSON_ TEXT NOT NULL DEFAULT '[]',
			TOOLS_JSON_ TEXT NOT NULL DEFAULT '[]',
			TAGS_ TEXT NOT NULL DEFAULT '',
			IMPORTANCE_ INTEGER NOT NULL DEFAULT 5,
			CONFIDENCE_ REAL NOT NULL DEFAULT 0.7,
			STATUS_ TEXT NOT NULL DEFAULT 'open',
			CREATED_AT_ INTEGER NOT NULL,
			UPDATED_AT_ INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS MEMORY_LINKS (
			ID_ TEXT PRIMARY KEY,
			FROM_ID_ TEXT NOT NULL,
			TO_ID_ TEXT NOT NULL,
			RELATION_TYPE_ TEXT NOT NULL DEFAULT 'supports',
			WEIGHT_ REAL NOT NULL DEFAULT 1.0,
			CREATED_AT_ INTEGER NOT NULL
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS MEMORIES_FTS USING fts5(
			SUMMARY_, SUBJECT_KEY_, CATEGORY_, TAGS_,
			content=MEMORIES, content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS MEMORIES_AI AFTER INSERT ON MEMORIES BEGIN
			INSERT INTO MEMORIES_FTS(rowid, SUMMARY_, SUBJECT_KEY_, CATEGORY_, TAGS_)
			VALUES (new.rowid, new.SUMMARY_, new.SUBJECT_KEY_, new.CATEGORY_, new.TAGS_);
		END`,
		`CREATE TRIGGER IF NOT EXISTS MEMORIES_AU AFTER UPDATE ON MEMORIES BEGIN
			INSERT INTO MEMORIES_FTS(MEMORIES_FTS, rowid, SUMMARY_, SUBJECT_KEY_, CATEGORY_, TAGS_)
			VALUES ('delete', old.rowid, old.SUMMARY_, old.SUBJECT_KEY_, old.CATEGORY_, old.TAGS_);
			INSERT INTO MEMORIES_FTS(rowid, SUMMARY_, SUBJECT_KEY_, CATEGORY_, TAGS_)
			VALUES (new.rowid, new.SUMMARY_, new.SUBJECT_KEY_, new.CATEGORY_, new.TAGS_);
		END`,
		`CREATE TRIGGER IF NOT EXISTS MEMORIES_AD AFTER DELETE ON MEMORIES BEGIN
			INSERT INTO MEMORIES_FTS(MEMORIES_FTS, rowid, SUMMARY_, SUBJECT_KEY_, CATEGORY_, TAGS_)
			VALUES ('delete', old.rowid, old.SUMMARY_, old.SUBJECT_KEY_, old.CATEGORY_, old.TAGS_);
		END`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	if err := s.ensureProjectionColumns(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error) {
	s.mu.Lock()
	history, err := s.listProjectionItemsLocked(agentKey)
	summarizer := s.summarizer
	s.mu.Unlock()
	if err != nil {
		return api.RememberResponse{}, err
	}
	drafts := summarizeRememberWithFallback(summarizer, RememberSynthesisInput{
		Request:  request,
		Chat:     chatDetail,
		AgentKey: agentKey,
		History:  history,
	})
	stored := buildRememberStoredItems(request, chatDetail, agentKey, drafts)
	for _, item := range stored {
		if err := s.Write(item); err != nil {
			return api.RememberResponse{}, err
		}
	}
	logMemoryOperation("remember", map[string]any{
		"agentKey":    agentKey,
		"chatId":      request.ChatID,
		"requestId":   request.RequestID,
		"memoryCount": len(stored),
	})

	memoryPath := filepath.Join(s.root, request.ChatID+".json")
	items := make([]api.RememberItemResponse, 0, len(stored))
	for _, item := range stored {
		items = append(items, api.RememberItemResponse{
			Summary:    item.Summary,
			SubjectKey: chatDetail.ChatID,
		})
	}
	payload := map[string]any{
		"requestId": request.RequestID,
		"chatId":    request.ChatID,
		"chatName":  chatDetail.ChatName,
		"items":     items,
		"stored":    stored,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return api.RememberResponse{}, err
	}
	if err := os.WriteFile(memoryPath, data, 0o644); err != nil {
		return api.RememberResponse{}, err
	}

	preview := &api.PromptPreviewResponse{
		UserPrompt:        firstRawMessage(chatDetail.RawMessages),
		ChatName:          chatDetail.ChatName,
		RawMessageCount:   len(chatDetail.RawMessages),
		EventCount:        len(chatDetail.Events),
		ReferenceCount:    len(chatDetail.References),
		RawMessageSamples: sampleMessages(chatDetail.RawMessages),
		EventSamples:      sampleEvents(chatDetail.Events),
	}

	return api.RememberResponse{
		Accepted:      len(stored) > 0,
		Status:        rememberStatus(stored),
		RequestID:     request.RequestID,
		ChatID:        request.ChatID,
		MemoryPath:    memoryPath,
		MemoryRoot:    s.root,
		MemoryCount:   len(stored),
		Detail:        "remember request captured; memory root=" + s.root,
		PromptPreview: preview,
		Items:         items,
		Stored:        stored,
	}, nil
}

func (s *SQLiteStore) Search(query string, limit int) ([]api.StoredMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 10
	}
	needle := strings.TrimSpace(query)
	if needle == "" {
		return s.listRecent(limit)
	}

	// FTS5 search
	ftsResults, err := s.ftsSearch(needle, limit*3)
	if err != nil {
		// Fallback to LIKE search on FTS failure
		ftsResults, _ = s.likeSearch(needle, limit*3)
	}

	// Score normalization and sorting
	if len(ftsResults) == 0 {
		logMemoryOperation("search", map[string]any{"query": query, "limit": limit, "count": 0})
		return []api.StoredMemoryResponse{}, nil
	}

	// Normalize FTS scores
	normalizeScores(ftsResults)

	// Sort by score desc, importance desc, updatedAt desc
	sort.SliceStable(ftsResults, func(i, j int) bool {
		if ftsResults[i].score != ftsResults[j].score {
			return ftsResults[i].score > ftsResults[j].score
		}
		if ftsResults[i].item.Importance != ftsResults[j].item.Importance {
			return ftsResults[i].item.Importance > ftsResults[j].item.Importance
		}
		return ftsResults[i].item.UpdatedAt > ftsResults[j].item.UpdatedAt
	})

	out := make([]api.StoredMemoryResponse, 0, limit)
	for i, r := range ftsResults {
		if i >= limit {
			break
		}
		// Update access tracking
		now := time.Now().UnixMilli()
		_, _ = s.db.Exec(
			`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			now, now, r.item.ID,
		)
		out = append(out, r.item)
	}
	logMemoryOperation("search", map[string]any{"query": query, "limit": limit, "count": len(out)})
	return out, nil
}

func (s *SQLiteStore) SearchDetailed(agentKey string, query string, category string, limit int) ([]ScoredRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = normalizeLimit(limit, 10)
	needle := strings.TrimSpace(query)
	if needle == "" {
		return []ScoredRecord{}, nil
	}
	normalizedCategory := normalizeOptionalCategory(category)
	results, err := s.ftsSearchDetailed(strings.TrimSpace(agentKey), normalizedCategory, needle, limit*3)
	if err != nil {
		results, err = s.likeSearchDetailed(strings.TrimSpace(agentKey), normalizedCategory, needle, limit*3)
		if err != nil {
			return nil, err
		}
	}
	if len(results) == 0 {
		return []ScoredRecord{}, nil
	}
	normalizeDetailedScores(results)
	out := make([]ScoredRecord, 0, min(len(results), limit))
	for i, result := range results {
		if i >= limit {
			break
		}
		now := time.Now().UnixMilli()
		_, _ = s.db.Exec(
			`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			now, now, result.memory.ID,
		)
		result.memory.AccessCount++
		result.memory.LastAccessedAt = &now
		result.memory.UpdatedAt = now
		out = append(out, ScoredRecord{
			Memory:    result.memory,
			Score:     result.score,
			MatchType: result.matchType,
		})
	}
	sortScoredRecords(out)
	logMemoryOperation("search_detailed", map[string]any{"agentKey": agentKey, "query": query, "category": category, "limit": limit, "count": len(out)})
	return out, nil
}

func (s *SQLiteStore) ReadDetail(agentKey string, id string) (*ToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT ID_, AGENT_KEY_, SUBJECT_KEY_, KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_, SUMMARY_, SOURCE_TYPE_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			EMBEDDING_MODEL_, TS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_,
			CASE WHEN EMBEDDING_ IS NULL THEN 0 ELSE 1 END
		FROM MEMORIES
		WHERE ID_ = ? AND (? = '' OR AGENT_KEY_ = ?)`,
		id, strings.TrimSpace(agentKey), strings.TrimSpace(agentKey),
	)
	record, err := scanToolRecord(row)
	if err == sql.ErrNoRows {
		logMemoryRead("read_detail", agentKey, id, false)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	_, _ = s.db.Exec(
		`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
		now, now, id,
	)
	record.AccessCount++
	record.LastAccessedAt = &now
	record.UpdatedAt = now
	logMemoryRead("read_detail", agentKey, id, true)
	return &record, nil
}

func (s *SQLiteStore) List(agentKey string, category string, limit int, sortBy string) ([]ToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = normalizeLimit(limit, 10)
	orderBy := "UPDATED_AT_ DESC, IMPORTANCE_ DESC"
	if normalizeSort(sortBy) == "importance" {
		orderBy = "IMPORTANCE_ DESC, UPDATED_AT_ DESC"
	}
	rows, err := s.db.Query(
		`SELECT ID_, AGENT_KEY_, SUBJECT_KEY_, KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_, SUMMARY_, SOURCE_TYPE_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			EMBEDDING_MODEL_, TS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_,
			CASE WHEN EMBEDDING_ IS NULL THEN 0 ELSE 1 END
		FROM MEMORIES
		WHERE (? = '' OR AGENT_KEY_ = ?) AND (? = '' OR CATEGORY_ = ?)
		ORDER BY `+orderBy+`
		LIMIT ?`,
		strings.TrimSpace(agentKey), strings.TrimSpace(agentKey), normalizeOptionalCategory(category), normalizeOptionalCategory(category), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]ToolRecord, 0)
	for rows.Next() {
		record, err := scanToolRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	err = rows.Err()
	if err == nil {
		logMemoryOperation("list", map[string]any{"agentKey": agentKey, "category": category, "limit": limit, "sort": sortBy, "count": len(records)})
	}
	return records, err
}

type scoredItem struct {
	item  api.StoredMemoryResponse
	score float64
}

type scoredToolItem struct {
	memory    ToolRecord
	score     float64
	matchType string
}

func (s *SQLiteStore) ftsSearch(query string, limit int) ([]scoredItem, error) {
	// Build FTS5 match expression: quote each term
	terms := strings.Fields(query)
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	matchExpr := strings.Join(quoted, " AND ")

	rows, err := s.db.Query(
		`SELECT m.ID_, m.TS_, m.REQUEST_ID_, m.CHAT_ID_, m.AGENT_KEY_, m.SUBJECT_KEY_,
			m.KIND_, m.REF_ID_, m.SCOPE_TYPE_, m.SCOPE_KEY_, m.TITLE_,
			m.SOURCE_TYPE_, m.SUMMARY_, m.CATEGORY_, m.IMPORTANCE_, m.CONFIDENCE_, m.STATUS_, m.TAGS_,
			m.UPDATED_AT_, m.ACCESS_COUNT_, m.LAST_ACCESSED_AT_,
			bm25(MEMORIES_FTS) as score
		FROM MEMORIES_FTS fts
		JOIN MEMORIES m ON m.rowid = fts.rowid
		WHERE MEMORIES_FTS MATCH ?
		ORDER BY score
		LIMIT ?`,
		matchExpr, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScoredRows(rows)
}

func (s *SQLiteStore) ftsSearchDetailed(agentKey string, category string, query string, limit int) ([]scoredToolItem, error) {
	terms := strings.Fields(query)
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	matchExpr := strings.Join(quoted, " AND ")

	rows, err := s.db.Query(
		`SELECT m.ID_, m.AGENT_KEY_, m.SUBJECT_KEY_, m.KIND_, m.REF_ID_, m.SCOPE_TYPE_, m.SCOPE_KEY_, m.TITLE_, m.SUMMARY_, m.SOURCE_TYPE_, m.CATEGORY_, m.IMPORTANCE_, m.CONFIDENCE_, m.STATUS_, m.TAGS_,
			m.EMBEDDING_MODEL_, m.TS_, m.UPDATED_AT_, m.ACCESS_COUNT_, m.LAST_ACCESSED_AT_,
			CASE WHEN m.EMBEDDING_ IS NULL THEN 0 ELSE 1 END,
			bm25(MEMORIES_FTS) as score
		FROM MEMORIES_FTS fts
		JOIN MEMORIES m ON m.rowid = fts.rowid
		WHERE MEMORIES_FTS MATCH ?
			AND (? = '' OR m.AGENT_KEY_ = ?)
			AND (? = '' OR m.CATEGORY_ = ?)
		ORDER BY score
		LIMIT ?`,
		matchExpr, agentKey, agentKey, category, category, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]scoredToolItem, 0)
	for rows.Next() {
		record, score, err := scanToolRecordWithScore(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, scoredToolItem{memory: record, score: math.Abs(score), matchType: "fts"})
	}
	return results, rows.Err()
}

func (s *SQLiteStore) likeSearch(query string, limit int) ([]scoredItem, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_,
			0 as score
		FROM MEMORIES
		WHERE SUMMARY_ LIKE ? OR SUBJECT_KEY_ LIKE ? OR CATEGORY_ LIKE ? OR TAGS_ LIKE ?
		ORDER BY UPDATED_AT_ DESC
		LIMIT ?`,
		pattern, pattern, pattern, pattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanScoredRows(rows)
}

func (s *SQLiteStore) likeSearchDetailed(agentKey string, category string, query string, limit int) ([]scoredToolItem, error) {
	pattern := "%" + query + "%"
	rows, err := s.db.Query(
		`SELECT ID_, AGENT_KEY_, SUBJECT_KEY_, KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_, SUMMARY_, SOURCE_TYPE_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			EMBEDDING_MODEL_, TS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_,
			CASE WHEN EMBEDDING_ IS NULL THEN 0 ELSE 1 END,
			0 as score
		FROM MEMORIES
		WHERE (? = '' OR AGENT_KEY_ = ?)
			AND (? = '' OR CATEGORY_ = ?)
			AND (SUMMARY_ LIKE ? OR SUBJECT_KEY_ LIKE ? OR CATEGORY_ LIKE ? OR TAGS_ LIKE ?)
		ORDER BY UPDATED_AT_ DESC, IMPORTANCE_ DESC
		LIMIT ?`,
		agentKey, agentKey, category, category, pattern, pattern, pattern, pattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]scoredToolItem, 0)
	for rows.Next() {
		record, score, err := scanToolRecordWithScore(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, scoredToolItem{memory: record, score: score, matchType: "like"})
	}
	return results, rows.Err()
}

func (s *SQLiteStore) listRecent(limit int) ([]api.StoredMemoryResponse, error) {
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		ORDER BY IMPORTANCE_ DESC, UPDATED_AT_ DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []api.StoredMemoryResponse
	for rows.Next() {
		item, err := scanMemoryRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanScoredRows(rows *sql.Rows) ([]scoredItem, error) {
	var results []scoredItem
	for rows.Next() {
		var item api.StoredMemoryResponse
		var ts int64
		var requestID, chatID, tags sql.NullString
		var kind, refID, scopeType, scopeKey, title, status sql.NullString
		var confidence sql.NullFloat64
		var accessCount sql.NullInt64
		var lastAccessedAt sql.NullInt64
		var score float64

		err := rows.Scan(
			&item.ID, &ts, &requestID, &chatID,
			&item.AgentKey, &item.SubjectKey, &kind, &refID, &scopeType, &scopeKey, &title,
			&item.SourceType, &item.Summary, &item.Category, &item.Importance, &confidence, &status, &tags,
			&item.UpdatedAt, &accessCount, &lastAccessedAt, &score,
		)
		if err != nil {
			return nil, err
		}
		item.CreatedAt = ts
		if requestID.Valid {
			item.RequestID = requestID.String
		}
		if chatID.Valid {
			item.ChatID = chatID.String
		}
		if kind.Valid {
			item.Kind = kind.String
		}
		if refID.Valid {
			item.RefID = refID.String
		}
		if scopeType.Valid {
			item.ScopeType = scopeType.String
		}
		if scopeKey.Valid {
			item.ScopeKey = scopeKey.String
		}
		if title.Valid {
			item.Title = title.String
		}
		if confidence.Valid {
			item.Confidence = confidence.Float64
		}
		if status.Valid {
			item.Status = status.String
		}
		if tags.Valid && tags.String != "" {
			item.Tags = strings.Split(tags.String, ",")
		}
		item = normalizeStoredItem(item)
		// BM25 returns negative scores (more negative = better match), convert to positive
		results = append(results, scoredItem{item: item, score: math.Abs(score)})
	}
	return results, rows.Err()
}

func scanMemoryRow(rows *sql.Rows) (api.StoredMemoryResponse, error) {
	var item api.StoredMemoryResponse
	var ts int64
	var requestID, chatID, tags sql.NullString
	var kind, refID, scopeType, scopeKey, title, status sql.NullString
	var confidence sql.NullFloat64
	var accessCount sql.NullInt64
	var lastAccessedAt sql.NullInt64

	err := rows.Scan(
		&item.ID, &ts, &requestID, &chatID,
		&item.AgentKey, &item.SubjectKey, &kind, &refID, &scopeType, &scopeKey, &title,
		&item.SourceType, &item.Summary, &item.Category, &item.Importance, &confidence, &status, &tags,
		&item.UpdatedAt, &accessCount, &lastAccessedAt,
	)
	if err != nil {
		return item, err
	}
	item.CreatedAt = ts
	if requestID.Valid {
		item.RequestID = requestID.String
	}
	if chatID.Valid {
		item.ChatID = chatID.String
	}
	if kind.Valid {
		item.Kind = kind.String
	}
	if refID.Valid {
		item.RefID = refID.String
	}
	if scopeType.Valid {
		item.ScopeType = scopeType.String
	}
	if scopeKey.Valid {
		item.ScopeKey = scopeKey.String
	}
	if title.Valid {
		item.Title = title.String
	}
	if confidence.Valid {
		item.Confidence = confidence.Float64
	}
	if status.Valid {
		item.Status = status.String
	}
	if tags.Valid && tags.String != "" {
		item.Tags = strings.Split(tags.String, ",")
	}
	if accessCount.Valid {
		item.AccessCount = int(accessCount.Int64)
	}
	if lastAccessedAt.Valid {
		v := lastAccessedAt.Int64
		item.LastAccessedAt = &v
	}
	return normalizeStoredItem(item), nil
}

func normalizeScores(items []scoredItem) {
	if len(items) <= 1 {
		return
	}
	minScore, maxScore := items[0].score, items[0].score
	for _, item := range items[1:] {
		if item.score < minScore {
			minScore = item.score
		}
		if item.score > maxScore {
			maxScore = item.score
		}
	}
	spread := maxScore - minScore
	if spread == 0 {
		for i := range items {
			items[i].score = 1.0
		}
		return
	}
	for i := range items {
		items[i].score = (items[i].score - minScore) / spread
	}
}

func normalizeDetailedScores(items []scoredToolItem) {
	if len(items) <= 1 {
		if len(items) == 1 {
			items[0].score = 1
		}
		return
	}
	minScore, maxScore := items[0].score, items[0].score
	for _, item := range items[1:] {
		if item.score < minScore {
			minScore = item.score
		}
		if item.score > maxScore {
			maxScore = item.score
		}
	}
	spread := maxScore - minScore
	if spread == 0 {
		for i := range items {
			items[i].score = 1
		}
		return
	}
	for i := range items {
		items[i].score = (items[i].score - minScore) / spread
	}
}

func (s *SQLiteStore) Read(id string) (*api.StoredMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES WHERE ID_ = ?`,
		id,
	)
	var item api.StoredMemoryResponse
	var ts int64
	var requestID, chatID, tags sql.NullString
	var kind, refID, scopeType, scopeKey, title, status sql.NullString
	var confidence sql.NullFloat64
	var accessCount sql.NullInt64
	var lastAccessedAt sql.NullInt64

	err := row.Scan(
		&item.ID, &ts, &requestID, &chatID,
		&item.AgentKey, &item.SubjectKey, &kind, &refID, &scopeType, &scopeKey, &title,
		&item.SourceType, &item.Summary, &item.Category, &item.Importance, &confidence, &status, &tags,
		&item.UpdatedAt, &accessCount, &lastAccessedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.CreatedAt = ts
	if requestID.Valid {
		item.RequestID = requestID.String
	}
	if chatID.Valid {
		item.ChatID = chatID.String
	}
	if kind.Valid {
		item.Kind = kind.String
	}
	if refID.Valid {
		item.RefID = refID.String
	}
	if scopeType.Valid {
		item.ScopeType = scopeType.String
	}
	if scopeKey.Valid {
		item.ScopeKey = scopeKey.String
	}
	if title.Valid {
		item.Title = title.String
	}
	if confidence.Valid {
		item.Confidence = confidence.Float64
	}
	if status.Valid {
		item.Status = status.String
	}
	if tags.Valid && tags.String != "" {
		item.Tags = strings.Split(tags.String, ",")
	}
	item = normalizeStoredItem(item)

	// Update access tracking
	now := time.Now().UnixMilli()
	_, _ = s.db.Exec(
		`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
		now, now, id,
	)
	logMemoryRead("read", "", id, true)
	return &item, nil
}

func (s *SQLiteStore) Write(item api.StoredMemoryResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.writeLocked(item)
	if err == nil {
		logMemoryWrite("write", normalizeStoredItem(item))
	}
	return err
}

func (s *SQLiteStore) BuildContextBundle(request ContextRequest) (ContextBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.listProjectionItemsLocked(strings.TrimSpace(request.AgentKey))
	if err != nil {
		return ContextBundle{}, err
	}

	hp := hybridParams{
		vectorWeight: s.ftsVectorWeight,
		ftsWeight:    s.ftsFTSWeight,
	}
	query := strings.TrimSpace(request.Query)
	if s.embedder != nil && query != "" {
		if qvec, err := s.embedder.EmbedSingle(context.Background(), query); err == nil {
			hp.queryEmbedding = qvec
			hp.itemEmbeddings = s.loadEmbeddingsLocked(items)
		}
	}

	bundle := buildContextBundleWithHybrid(request, items, hp)
	logMemoryOperation("build_context_bundle", map[string]any{
		"agentKey":         request.AgentKey,
		"teamId":           request.TeamID,
		"chatId":           request.ChatID,
		"userKey":          request.UserKey,
		"query":            request.Query,
		"totalCandidates":  len(items),
		"stableFacts":      len(bundle.StableFacts),
		"sessionItems":     len(bundle.SessionSummaries),
		"observations":     len(bundle.RelevantObservations),
		"stableChars":      len(bundle.StablePrompt),
		"sessionChars":     len(bundle.SessionPrompt),
		"observationChars": len(bundle.ObservationPrompt),
		"layers":           bundle.DisclosedLayers,
		"stopReason":       bundle.StopReason,
		"hybrid":           len(hp.queryEmbedding) > 0,
		"maxChars":         request.MaxChars,
	})
	return bundle, nil
}

func (s *SQLiteStore) loadEmbeddingsLocked(items []api.StoredMemoryResponse) map[string][]float64 {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if normalizeMemoryKind(item.Kind) == KindObservation {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT ID_, EMBEDDING_ FROM MEMORIES WHERE ID_ IN (`+strings.Join(placeholders, ",")+`) AND EMBEDDING_ IS NOT NULL`,
		args...,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string][]float64)
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			continue
		}
		var vec []float64
		if err := json.Unmarshal(blob, &vec); err != nil {
			continue
		}
		result[id] = vec
	}
	return result
}

func (s *SQLiteStore) Learn(input LearnInput) (api.LearnResponse, error) {
	s.mu.Lock()
	history, err := s.listProjectionItemsLocked(input.AgentKey)
	summarizer := s.summarizer
	s.mu.Unlock()
	if err != nil {
		return api.LearnResponse{}, err
	}
	drafts := summarizeLearnWithFallback(summarizer, LearnSynthesisInput{
		Request:  input.Request,
		Trace:    input.Trace,
		AgentKey: input.AgentKey,
		TeamID:   input.TeamID,
		UserKey:  input.UserKey,
		History:  history,
	})
	stored := buildLearnedMemoriesFromDrafts(input, drafts)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range stored {
		if err := s.writeLocked(item); err != nil {
			return api.LearnResponse{}, err
		}
	}
	response := buildLearnResponse(input, stored)
	if len(stored) == 0 {
		response.Accepted = false
	}
	autoConsolidation := ConsolidationResult{}
	candidateCount := 0
	if input.SkillCandidates != nil {
		if candidate, ok := skills.CandidateFromRunTrace(input.Trace, input.AgentKey, input.Request.ChatID); ok {
			if _, err := input.SkillCandidates.Write(candidate); err != nil {
				return api.LearnResponse{}, err
			}
			candidateCount = 1
		}
	}
	if strings.TrimSpace(input.AgentKey) != "" && len(stored) > 0 {
		items, err := s.listProjectionItemsLocked(input.AgentKey)
		if err != nil {
			return api.LearnResponse{}, err
		}
		autoConsolidation, err = s.applyConsolidationPlanLocked(input.AgentKey, buildObservationConsolidationPlanWithMode(input.AgentKey, items, time.Now(), false))
		if err != nil {
			return api.LearnResponse{}, err
		}
	}
	logMemoryOperation("learn", map[string]any{
		"agentKey":            input.AgentKey,
		"chatId":              input.Request.ChatID,
		"requestId":           input.Request.RequestID,
		"observationCount":    len(stored),
		"skillCandidateCount": candidateCount,
		"archivedCount":       autoConsolidation.ArchivedCount,
		"mergedCount":         autoConsolidation.MergedCount,
		"promotedCount":       autoConsolidation.PromotedCount,
		"accepted":            response.Accepted,
	})
	return response, nil
}

func (s *SQLiteStore) Consolidate(agentKey string) (ConsolidationResult, error) {
	s.mu.Lock()
	items, err := s.listProjectionItemsLocked(agentKey)
	if err != nil {
		s.mu.Unlock()
		return ConsolidationResult{}, err
	}
	result, err := s.applyConsolidationPlanLocked(agentKey, buildConsolidationPlan(agentKey, items, time.Now()))
	s.mu.Unlock()
	return result, err
}

func (s *SQLiteStore) applyConsolidationPlanLocked(agentKey string, plan consolidationPlan) (ConsolidationResult, error) {
	result := ConsolidationResult{}
	for id := range plan.archiveIDs {
		record, err := s.updateLocked(agentKey, MutationInput{ID: id, Status: ptrString(StatusArchived)})
		if err != nil {
			return result, err
		}
		if record != nil {
			result.ArchivedCount++
			if _, merged := plan.mergeIDs[id]; merged {
				result.MergedCount++
			}
		}
	}
	for id, keeperID := range plan.supersedeIDs {
		record, err := s.updateLocked(agentKey, MutationInput{ID: id, Status: ptrString(StatusSuperseded)})
		if err != nil {
			return result, err
		}
		if record != nil {
			result.MergedCount++
			if err := s.insertMemoryLinkLocked(keeperID, id, "supersedes", 1.0); err != nil {
				return result, err
			}
		}
	}
	for _, id := range plan.promoteIDs {
		record, err := s.promoteLocked(agentKey, PromoteInput{
			SourceID:      id,
			ScopeType:     ScopeAgent,
			ArchiveSource: true,
		})
		if err != nil {
			return result, err
		}
		if record != nil {
			result.PromotedCount++
			result.ArchivedCount++
		}
	}
	logMemoryOperation("consolidate", map[string]any{
		"agentKey":      agentKey,
		"archivedCount": result.ArchivedCount,
		"mergedCount":   result.MergedCount,
		"promotedCount": result.PromotedCount,
	})
	return result, nil
}

func (s *SQLiteStore) Update(agentKey string, input MutationInput) (*ToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.updateLocked(agentKey, input)
}

func (s *SQLiteStore) updateLocked(agentKey string, input MutationInput) (*ToolRecord, error) {

	current, err := s.readProjectionByIDLocked(strings.TrimSpace(input.ID))
	if err != nil || current == nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(current.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	if input.Title != nil {
		current.Title = strings.TrimSpace(*input.Title)
	}
	if input.Summary != nil {
		current.Summary = strings.TrimSpace(*input.Summary)
	}
	if input.Category != nil {
		current.Category = normalizeCategory(*input.Category)
	}
	if input.ScopeType != nil {
		current.ScopeType = normalizeScopeType(*input.ScopeType)
	}
	if input.ScopeKey != nil {
		current.ScopeKey = strings.TrimSpace(*input.ScopeKey)
	}
	if input.Status != nil {
		current.Status = normalizeMemoryStatus(*input.Status, current.Kind)
	}
	if input.Importance != nil {
		current.Importance = normalizeImportance(*input.Importance)
	}
	if input.Confidence != nil {
		current.Confidence = normalizeMemoryConfidence(*input.Confidence, current.Kind)
	}
	if input.ReplaceTags {
		current.Tags = normalizeTags(input.Tags)
	}
	current.UpdatedAt = time.Now().UnixMilli()
	if err := s.writeLocked(*current); err != nil {
		return nil, err
	}
	record := toolRecordFromStored(*current)
	logMemoryOperation("update", map[string]any{"agentKey": agentKey, "id": input.ID, "status": record.Status})
	return &record, nil
}

func (s *SQLiteStore) Forget(agentKey string, id string, status string) (*ToolRecord, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		status = StatusArchived
	}
	record, err := s.Update(agentKey, MutationInput{
		ID:     strings.TrimSpace(id),
		Status: &status,
	})
	if err == nil && record != nil {
		logMemoryOperation("forget", map[string]any{"agentKey": agentKey, "id": id, "status": record.Status})
	}
	return record, err
}

func (s *SQLiteStore) Timeline(agentKey string, id string, limit int) ([]TimelineEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = normalizeLimit(limit, 10)
	item, err := s.readProjectionByIDLocked(id)
	if err != nil || item == nil {
		return nil, err
	}
	record := toolRecordFromStored(*item)
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(record.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	entries := []TimelineEntry{{
		Memory:       record,
		RelationType: "self",
		Direction:    "self",
	}}
	rows, err := s.db.Query(
		`SELECT l.FROM_ID_, l.TO_ID_, l.RELATION_TYPE_,
			m.ID_, m.AGENT_KEY_, m.SUBJECT_KEY_, m.KIND_, m.REF_ID_, m.SCOPE_TYPE_, m.SCOPE_KEY_, m.TITLE_, m.SUMMARY_, m.SOURCE_TYPE_,
			m.CATEGORY_, m.IMPORTANCE_, m.CONFIDENCE_, m.STATUS_, m.TAGS_, m.EMBEDDING_MODEL_, m.TS_, m.UPDATED_AT_, m.ACCESS_COUNT_, m.LAST_ACCESSED_AT_,
			CASE WHEN m.EMBEDDING_ IS NULL THEN 0 ELSE 1 END
		FROM MEMORY_LINKS l
		JOIN MEMORIES m ON m.ID_ = CASE WHEN l.FROM_ID_ = ? THEN l.TO_ID_ ELSE l.FROM_ID_ END
		WHERE l.FROM_ID_ = ? OR l.TO_ID_ = ?
		ORDER BY l.CREATED_AT_ DESC
		LIMIT ?`,
		id, id, id, limit-1,
	)
	if err != nil {
		return entries, nil
	}
	defer rows.Close()
	for rows.Next() {
		var fromID, toID, relationType string
		record, err := scanToolRecord(linkScanner{scanner: rows, fromID: &fromID, toID: &toID, relationType: &relationType})
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(record.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		direction := "peer"
		if strings.TrimSpace(fromID) == strings.TrimSpace(id) {
			direction = "outgoing"
		} else if strings.TrimSpace(toID) == strings.TrimSpace(id) {
			direction = "incoming"
		}
		entries = append(entries, TimelineEntry{
			Memory:       record,
			RelationType: relationType,
			Direction:    direction,
		})
	}
	err = rows.Err()
	if err == nil {
		logMemoryOperation("timeline", map[string]any{"agentKey": agentKey, "id": id, "limit": limit, "count": len(entries)})
	}
	return entries, err
}

func (s *SQLiteStore) Promote(agentKey string, input PromoteInput) (*ToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.promoteLocked(agentKey, input)
}

func (s *SQLiteStore) promoteLocked(agentKey string, input PromoteInput) (*ToolRecord, error) {

	source, err := s.readProjectionByIDLocked(strings.TrimSpace(input.SourceID))
	if err != nil || source == nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(source.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	if normalizeMemoryKind(source.Kind) != KindObservation {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         generateMemoryID(),
		RequestID:  source.RequestID,
		ChatID:     source.ChatID,
		AgentKey:   source.AgentKey,
		SubjectKey: normalizeSubjectKey("", source.ChatID, source.AgentKey),
		Kind:       KindFact,
		RefID:      source.ID,
		ScopeType:  normalizeScopeType(input.ScopeType),
		ScopeKey:   strings.TrimSpace(input.ScopeKey),
		Title:      normalizeMemoryTitle(input.Title, firstNonBlank(input.Summary, source.Title, source.Summary)),
		Summary:    firstNonBlank(input.Summary, source.Summary),
		SourceType: "promote",
		Category:   normalizeCategory(firstNonBlank(input.Category, source.Category)),
		Importance: normalizeImportance(firstPositive(input.Importance, source.Importance)),
		Confidence: normalizeMemoryConfidence(firstPositiveFloat(input.Confidence, source.Confidence), KindFact),
		Status:     StatusActive,
		Tags:       normalizeTags(append([]string{"promoted"}, chooseTags(input.Tags, source.Tags)...)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if item.ScopeType == "" || item.ScopeType == ScopeChat {
		item.ScopeType = ScopeAgent
	}
	item.ScopeKey = normalizeScopeKey(item.ScopeType, item.ScopeKey, item.AgentKey, "", item.ChatID, "")
	if err := s.writeLocked(item); err != nil {
		return nil, err
	}
	if err := s.insertMemoryLinkLocked(item.ID, source.ID, "derived_from", 1.0); err != nil {
		return nil, err
	}
	if input.ArchiveSource {
		source.Status = StatusArchived
		source.UpdatedAt = time.Now().UnixMilli()
		if err := s.writeLocked(*source); err != nil {
			return nil, err
		}
	}
	record := toolRecordFromStored(item)
	logMemoryOperation("promote", map[string]any{"agentKey": agentKey, "sourceId": input.SourceID, "id": item.ID, "archiveSource": input.ArchiveSource})
	return &record, nil
}

func ptrString(value string) *string {
	return &value
}

func (s *SQLiteStore) writeLocked(item api.StoredMemoryResponse) error {
	if item.ID == "" {
		item.ID = generateMemoryID()
	}
	now := time.Now().UnixMilli()
	if item.UpdatedAt == 0 {
		item.UpdatedAt = now
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = item.UpdatedAt
	}
	item = normalizeStoredItem(item)
	if err := validateStoredMemoryItem(item); err != nil {
		logMemoryWriteRejected("write_rejected", item, err)
		return err
	}
	if strings.TrimSpace(item.ScopeKey) == "" {
		item.ScopeKey = normalizeScopeKey(item.ScopeType, "", item.AgentKey, "", item.ChatID, "")
	}
	if existing, err := s.findExactDuplicateLocked(item); err != nil {
		return err
	} else if existing != nil {
		return s.bumpDuplicateMemoryLocked(*existing, item, now)
	}
	if existing, err := s.findNearDuplicateFactLocked(item); err != nil {
		return err
	} else if existing != nil {
		return s.mergeNearDuplicateFactLocked(*existing, item, now)
	}
	if normalizeMemoryKind(item.Kind) == KindFact {
		if err := s.supersedeMatchingFactsLocked(item); err != nil {
			return err
		}
	}
	if err := s.upsertProjectionLocked(item); err != nil {
		return err
	}
	if normalizeMemoryKind(item.Kind) == KindObservation {
		if err := s.upsertObservationSourceLocked(item); err != nil {
			return err
		}
	} else {
		if err := s.upsertFactSourceLocked(item); err != nil {
			return err
		}
	}
	if s.dualWriteMD {
		_ = AppendJournal(s.root, item)
	}
	if s.embedder != nil {
		text := strings.TrimSpace(item.Title + " " + item.Summary)
		if text != "" {
			if vec, err := s.embedder.EmbedSingle(context.Background(), text); err == nil {
				if blob, err := json.Marshal(vec); err == nil {
					_, _ = s.db.Exec(
						`UPDATE MEMORIES SET EMBEDDING_ = ?, EMBEDDING_MODEL_ = ? WHERE ID_ = ?`,
						blob, s.embedder.Model, item.ID,
					)
				}
			}
		}
	}
	_ = s.refreshSnapshotsLocked(item.AgentKey)
	return nil
}

func (s *SQLiteStore) findExactDuplicateLocked(item api.StoredMemoryResponse) (*api.StoredMemoryResponse, error) {
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		WHERE ID_ != ?
			AND AGENT_KEY_ = ?
			AND KIND_ = ?
			AND SCOPE_TYPE_ = ?
			AND SCOPE_KEY_ = ?
			AND CATEGORY_ = ?
			AND TITLE_ = ?
			AND SUMMARY_ = ?
			AND STATUS_ = ?
			AND ((? = '' AND CHAT_ID_ = '') OR CHAT_ID_ = ?)
		ORDER BY UPDATED_AT_ DESC
		LIMIT 1`,
		item.ID,
		item.AgentKey,
		item.Kind,
		item.ScopeType,
		item.ScopeKey,
		item.Category,
		item.Title,
		item.Summary,
		item.Status,
		item.ChatID, item.ChatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	existing, err := scanMemoryRow(rows)
	if err != nil {
		return nil, err
	}
	return &existing, rows.Err()
}

func (s *SQLiteStore) bumpDuplicateMemoryLocked(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse, now int64) error {
	existing.Importance = max(existing.Importance, incoming.Importance)
	existing.Confidence = maxFloat(existing.Confidence, incoming.Confidence)
	existing.Tags = normalizeTags(append(existing.Tags, incoming.Tags...))
	existing.UpdatedAt = now
	existing.AccessCount++
	existing.LastAccessedAt = &now
	if err := s.upsertProjectionLocked(existing); err != nil {
		return err
	}
	if normalizeMemoryKind(existing.Kind) == KindObservation {
		if err := s.upsertObservationSourceLocked(existing); err != nil {
			return err
		}
	} else {
		if err := s.upsertFactSourceLocked(existing); err != nil {
			return err
		}
	}
	_ = s.refreshSnapshotsLocked(existing.AgentKey)
	logMemoryOperation("write_duplicate", map[string]any{
		"id":          existing.ID,
		"incomingId":  incoming.ID,
		"agentKey":    existing.AgentKey,
		"kind":        existing.Kind,
		"scopeType":   existing.ScopeType,
		"scopeKey":    existing.ScopeKey,
		"category":    existing.Category,
		"accessCount": existing.AccessCount,
	})
	return nil
}

func (s *SQLiteStore) findNearDuplicateFactLocked(item api.StoredMemoryResponse) (*api.StoredMemoryResponse, error) {
	if normalizeMemoryKind(item.Kind) != KindFact {
		return nil, nil
	}
	if normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		WHERE ID_ != ?
			AND AGENT_KEY_ = ?
			AND KIND_ = ?
			AND SCOPE_TYPE_ = ?
			AND SCOPE_KEY_ = ?
			AND CATEGORY_ = ?
			AND STATUS_ = ?
		ORDER BY IMPORTANCE_ DESC, CONFIDENCE_ DESC, UPDATED_AT_ DESC`,
		item.ID,
		item.AgentKey,
		KindFact,
		item.ScopeType,
		item.ScopeKey,
		item.Category,
		StatusActive,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		existing, err := scanMemoryRow(rows)
		if err != nil {
			return nil, err
		}
		if isNearDuplicateFactMemory(existing, item) {
			return &existing, nil
		}
	}
	return nil, rows.Err()
}

func (s *SQLiteStore) mergeNearDuplicateFactLocked(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse, now int64) error {
	merged := mergeNearDuplicateFactMemory(existing, incoming, now)
	if err := s.upsertProjectionLocked(merged); err != nil {
		return err
	}
	if err := s.upsertFactSourceLocked(merged); err != nil {
		return err
	}
	_ = s.refreshSnapshotsLocked(existing.AgentKey)
	logMemoryOperation("write_near_duplicate_fact", map[string]any{
		"id":         existing.ID,
		"incomingId": incoming.ID,
		"agentKey":   existing.AgentKey,
		"scopeType":  existing.ScopeType,
		"scopeKey":   existing.ScopeKey,
		"category":   existing.Category,
	})
	return nil
}

func maxFloat(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (s *SQLiteStore) readProjectionByIDLocked(id string) (*api.StoredMemoryResponse, error) {
	row := s.db.QueryRow(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES WHERE ID_ = ?`,
		id,
	)
	var item api.StoredMemoryResponse
	var ts int64
	var requestID, chatID, tags sql.NullString
	var kind, refID, scopeType, scopeKey, title, status sql.NullString
	var confidence sql.NullFloat64
	var accessCount sql.NullInt64
	var lastAccessedAt sql.NullInt64
	err := row.Scan(
		&item.ID, &ts, &requestID, &chatID,
		&item.AgentKey, &item.SubjectKey, &kind, &refID, &scopeType, &scopeKey, &title,
		&item.SourceType, &item.Summary, &item.Category, &item.Importance, &confidence, &status, &tags,
		&item.UpdatedAt, &accessCount, &lastAccessedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.CreatedAt = ts
	if requestID.Valid {
		item.RequestID = requestID.String
	}
	if chatID.Valid {
		item.ChatID = chatID.String
	}
	if kind.Valid {
		item.Kind = kind.String
	}
	if refID.Valid {
		item.RefID = refID.String
	}
	if scopeType.Valid {
		item.ScopeType = scopeType.String
	}
	if scopeKey.Valid {
		item.ScopeKey = scopeKey.String
	}
	if title.Valid {
		item.Title = title.String
	}
	if confidence.Valid {
		item.Confidence = confidence.Float64
	}
	if status.Valid {
		item.Status = status.String
	}
	if tags.Valid && tags.String != "" {
		item.Tags = strings.Split(tags.String, ",")
	}
	normalized := normalizeStoredItem(item)
	return &normalized, nil
}

func (s *SQLiteStore) supersedeMatchingFactsLocked(item api.StoredMemoryResponse) error {
	if normalizeMemoryKind(item.Kind) != KindFact {
		return nil
	}
	if normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
		return nil
	}
	dedupeKey := factDedupeKey(item)
	rows, err := s.db.Query(
		`SELECT ID_ FROM MEMORY_FACTS
		WHERE DEDUPE_KEY_ = ? AND ID_ != ? AND STATUS_ = ?`,
		dedupeKey, item.ID, StatusActive,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var priorIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if strings.TrimSpace(id) != "" {
			priorIDs = append(priorIDs, strings.TrimSpace(id))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, priorID := range priorIDs {
		now := time.Now().UnixMilli()
		if _, err := s.db.Exec(
			`UPDATE MEMORY_FACTS SET STATUS_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			StatusSuperseded, now, priorID,
		); err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`UPDATE MEMORIES SET STATUS_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			StatusSuperseded, now, priorID,
		); err != nil {
			return err
		}
		if err := s.insertMemoryLinkLocked(item.ID, priorID, "supersedes", 1.0); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) upsertProjectionLocked(item api.StoredMemoryResponse) error {
	tagsStr := strings.Join(item.Tags, ",")
	_, err := s.db.Exec(
		`INSERT INTO MEMORIES (ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			CHAT_ID_ = excluded.CHAT_ID_,
			AGENT_KEY_ = excluded.AGENT_KEY_,
			SUBJECT_KEY_ = excluded.SUBJECT_KEY_,
			KIND_ = excluded.KIND_,
			REF_ID_ = excluded.REF_ID_,
			SCOPE_TYPE_ = excluded.SCOPE_TYPE_,
			SCOPE_KEY_ = excluded.SCOPE_KEY_,
			TITLE_ = excluded.TITLE_,
			SOURCE_TYPE_ = excluded.SOURCE_TYPE_,
			SUMMARY_ = excluded.SUMMARY_,
			CATEGORY_ = excluded.CATEGORY_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			CONFIDENCE_ = excluded.CONFIDENCE_,
			STATUS_ = excluded.STATUS_,
			TAGS_ = excluded.TAGS_,
			UPDATED_AT_ = excluded.UPDATED_AT_,
			ACCESS_COUNT_ = excluded.ACCESS_COUNT_,
			LAST_ACCESSED_AT_ = excluded.LAST_ACCESSED_AT_`,
		item.ID, item.CreatedAt, item.RequestID, item.ChatID,
		item.AgentKey, item.SubjectKey, item.Kind, item.RefID, item.ScopeType, item.ScopeKey, item.Title,
		item.SourceType, item.Summary, item.Category, item.Importance, item.Confidence, item.Status, tagsStr, item.UpdatedAt, item.AccessCount, item.LastAccessedAt,
	)
	return err
}

func (s *SQLiteStore) upsertFactSourceLocked(item api.StoredMemoryResponse) error {
	tagsStr := strings.Join(item.Tags, ",")
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_FACTS (ID_, AGENT_KEY_, SCOPE_TYPE_, SCOPE_KEY_, CATEGORY_, TITLE_, CONTENT_,
			TAGS_, IMPORTANCE_, CONFIDENCE_, STATUS_, SOURCE_KIND_, SOURCE_REF_, DEDUPE_KEY_, CREATED_AT_, UPDATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			AGENT_KEY_ = excluded.AGENT_KEY_,
			SCOPE_TYPE_ = excluded.SCOPE_TYPE_,
			SCOPE_KEY_ = excluded.SCOPE_KEY_,
			CATEGORY_ = excluded.CATEGORY_,
			TITLE_ = excluded.TITLE_,
			CONTENT_ = excluded.CONTENT_,
			TAGS_ = excluded.TAGS_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			CONFIDENCE_ = excluded.CONFIDENCE_,
			STATUS_ = excluded.STATUS_,
			SOURCE_KIND_ = excluded.SOURCE_KIND_,
			SOURCE_REF_ = excluded.SOURCE_REF_,
			DEDUPE_KEY_ = excluded.DEDUPE_KEY_,
			UPDATED_AT_ = excluded.UPDATED_AT_`,
		item.ID, item.AgentKey, item.ScopeType, item.ScopeKey, item.Category, item.Title, item.Summary,
		tagsStr, item.Importance, item.Confidence, item.Status, item.SourceType, item.RefID, factDedupeKey(item), item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *SQLiteStore) upsertObservationSourceLocked(item api.StoredMemoryResponse) error {
	tagsStr := strings.Join(item.Tags, ",")
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_OBSERVATIONS (ID_, AGENT_KEY_, CHAT_ID_, RUN_ID_, SCOPE_TYPE_, SCOPE_KEY_, TYPE_, TITLE_, SUMMARY_, DETAIL_,
			FILES_JSON_, TOOLS_JSON_, TAGS_, IMPORTANCE_, CONFIDENCE_, STATUS_, CREATED_AT_, UPDATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			AGENT_KEY_ = excluded.AGENT_KEY_,
			CHAT_ID_ = excluded.CHAT_ID_,
			RUN_ID_ = excluded.RUN_ID_,
			SCOPE_TYPE_ = excluded.SCOPE_TYPE_,
			SCOPE_KEY_ = excluded.SCOPE_KEY_,
			TYPE_ = excluded.TYPE_,
			TITLE_ = excluded.TITLE_,
			SUMMARY_ = excluded.SUMMARY_,
			DETAIL_ = excluded.DETAIL_,
			TAGS_ = excluded.TAGS_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			CONFIDENCE_ = excluded.CONFIDENCE_,
			STATUS_ = excluded.STATUS_,
			UPDATED_AT_ = excluded.UPDATED_AT_`,
		item.ID, item.AgentKey, item.ChatID, item.RefID, item.ScopeType, item.ScopeKey, item.Category, item.Title, item.Summary, item.Summary,
		tagsStr, item.Importance, item.Confidence, item.Status, item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *SQLiteStore) listProjectionItemsLocked(agentKey string) ([]api.StoredMemoryResponse, error) {
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		WHERE (? = '' OR AGENT_KEY_ = ? OR AGENT_KEY_ = '')
		ORDER BY UPDATED_AT_ DESC, IMPORTANCE_ DESC`,
		agentKey, agentKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]api.StoredMemoryResponse, 0)
	for rows.Next() {
		item, err := scanMemoryRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) refreshSnapshotsLocked(agentKey string) error {
	items, err := s.listProjectionItemsLocked(strings.TrimSpace(agentKey))
	if err != nil {
		return err
	}
	return refreshSnapshots(s.root, agentKey, items)
}

func factDedupeKey(item api.StoredMemoryResponse) string {
	title := strings.ToLower(strings.TrimSpace(item.Title))
	if title == "" {
		title = strings.ToLower(strings.TrimSpace(item.Summary))
	}
	return fmt.Sprintf("%s|%s|%s", normalizeScopeType(item.ScopeType), strings.TrimSpace(item.ScopeKey), title)
}

func (s *SQLiteStore) insertMemoryLinkLocked(fromID string, toID string, relationType string, weight float64) error {
	if strings.TrimSpace(fromID) == "" || strings.TrimSpace(toID) == "" || strings.TrimSpace(relationType) == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_LINKS (ID_, FROM_ID_, TO_ID_, RELATION_TYPE_, WEIGHT_, CREATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO NOTHING`,
		generateMemoryID(),
		strings.TrimSpace(fromID),
		strings.TrimSpace(toID),
		strings.TrimSpace(relationType),
		weight,
		time.Now().UnixMilli(),
	)
	return err
}

func (s *SQLiteStore) ensureProjectionColumns() error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "KIND_", definition: "TEXT NOT NULL DEFAULT 'fact'"},
		{name: "REF_ID_", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "SCOPE_TYPE_", definition: "TEXT NOT NULL DEFAULT 'agent'"},
		{name: "SCOPE_KEY_", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "TITLE_", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "CONFIDENCE_", definition: "REAL NOT NULL DEFAULT 0.9"},
		{name: "STATUS_", definition: "TEXT NOT NULL DEFAULT 'active'"},
	}
	for _, column := range columns {
		if err := ensureSQLiteColumn(s.db, "MEMORIES", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

type sqlScanner interface {
	Scan(dest ...any) error
}

type linkScanner struct {
	scanner      sqlScanner
	fromID       *string
	toID         *string
	relationType *string
}

func (s linkScanner) Scan(dest ...any) error {
	values := make([]any, 0, len(dest)+3)
	values = append(values, s.fromID, s.toID, s.relationType)
	values = append(values, dest...)
	return s.scanner.Scan(values...)
}

func scanToolRecord(scanner sqlScanner) (ToolRecord, error) {
	var record ToolRecord
	var tags sql.NullString
	var kind, refID, scopeType, scopeKey, title, status sql.NullString
	var confidence sql.NullFloat64
	var embeddingModel sql.NullString
	var lastAccessedAt sql.NullInt64
	var hasEmbedding int

	err := scanner.Scan(
		&record.ID, &record.AgentKey, &record.SubjectKey, &kind, &refID, &scopeType, &scopeKey, &title, &record.Content, &record.SourceType,
		&record.Category, &record.Importance, &confidence, &status, &tags, &embeddingModel, &record.CreatedAt,
		&record.UpdatedAt, &record.AccessCount, &lastAccessedAt, &hasEmbedding,
	)
	if err != nil {
		return record, err
	}
	if tags.Valid && tags.String != "" {
		record.Tags = strings.Split(tags.String, ",")
	} else {
		record.Tags = []string{}
	}
	if kind.Valid {
		record.Kind = kind.String
	}
	if refID.Valid {
		record.RefID = refID.String
	}
	if scopeType.Valid {
		record.ScopeType = scopeType.String
	}
	if scopeKey.Valid {
		record.ScopeKey = scopeKey.String
	}
	if title.Valid {
		record.Title = title.String
	}
	if confidence.Valid {
		record.Confidence = confidence.Float64
	}
	if status.Valid {
		record.Status = status.String
	}
	if embeddingModel.Valid {
		value := embeddingModel.String
		record.EmbeddingModel = &value
	}
	record.HasEmbedding = hasEmbedding != 0
	if lastAccessedAt.Valid {
		value := lastAccessedAt.Int64
		record.LastAccessedAt = &value
	}
	record.Kind = normalizeMemoryKind(record.Kind)
	record.ScopeType = normalizeScopeType(record.ScopeType)
	record.Title = normalizeMemoryTitle(record.Title, record.Content)
	record.Status = normalizeMemoryStatus(record.Status, record.Kind)
	record.Confidence = normalizeMemoryConfidence(record.Confidence, record.Kind)
	if strings.TrimSpace(record.RefID) == "" {
		record.RefID = record.ID
	}
	return record, nil
}

func scanToolRecordWithScore(scanner sqlScanner) (ToolRecord, float64, error) {
	var record ToolRecord
	var tags sql.NullString
	var kind, refID, scopeType, scopeKey, title, status sql.NullString
	var confidence sql.NullFloat64
	var embeddingModel sql.NullString
	var lastAccessedAt sql.NullInt64
	var hasEmbedding int
	var score float64

	err := scanner.Scan(
		&record.ID, &record.AgentKey, &record.SubjectKey, &kind, &refID, &scopeType, &scopeKey, &title, &record.Content, &record.SourceType,
		&record.Category, &record.Importance, &confidence, &status, &tags, &embeddingModel, &record.CreatedAt,
		&record.UpdatedAt, &record.AccessCount, &lastAccessedAt, &hasEmbedding, &score,
	)
	if err != nil {
		return record, 0, err
	}
	if tags.Valid && tags.String != "" {
		record.Tags = strings.Split(tags.String, ",")
	} else {
		record.Tags = []string{}
	}
	if kind.Valid {
		record.Kind = kind.String
	}
	if refID.Valid {
		record.RefID = refID.String
	}
	if scopeType.Valid {
		record.ScopeType = scopeType.String
	}
	if scopeKey.Valid {
		record.ScopeKey = scopeKey.String
	}
	if title.Valid {
		record.Title = title.String
	}
	if confidence.Valid {
		record.Confidence = confidence.Float64
	}
	if status.Valid {
		record.Status = status.String
	}
	if embeddingModel.Valid {
		value := embeddingModel.String
		record.EmbeddingModel = &value
	}
	record.HasEmbedding = hasEmbedding != 0
	if lastAccessedAt.Valid {
		value := lastAccessedAt.Int64
		record.LastAccessedAt = &value
	}
	record.Kind = normalizeMemoryKind(record.Kind)
	record.ScopeType = normalizeScopeType(record.ScopeType)
	record.Title = normalizeMemoryTitle(record.Title, record.Content)
	record.Status = normalizeMemoryStatus(record.Status, record.Kind)
	record.Confidence = normalizeMemoryConfidence(record.Confidence, record.Kind)
	if strings.TrimSpace(record.RefID) == "" {
		record.RefID = record.ID
	}
	return record, score, nil
}

func ensureSQLiteColumn(db *sql.DB, table string, column string, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(column)) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func generateMemoryID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "mem_" + hex.EncodeToString(b)
}
