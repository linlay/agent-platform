package memory

import (
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

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	root           string
	dbPath         string
	dualWriteMD    bool
	mu             sync.Mutex
	db             *sql.DB
	ftsVectorWeight float64
	ftsFTSWeight    float64
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
	return nil
}

func (s *SQLiteStore) Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error) {
	now := time.Now().UnixMilli()
	summary := extractRememberSummary(chatDetail)
	item := api.RememberItemResponse{
		Summary:    summary,
		SubjectKey: chatDetail.ChatID,
	}
	id := generateMemoryID()
	stored := api.StoredMemoryResponse{
		ID:         id,
		RequestID:  request.RequestID,
		ChatID:     request.ChatID,
		AgentKey:   agentKey,
		SubjectKey: chatDetail.ChatID,
		Summary:    summary,
		SourceType: "remember",
		Category:   "remember",
		Importance: 6,
		Tags:       []string{"remember"},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.Write(stored); err != nil {
		return api.RememberResponse{}, err
	}

	memoryPath := filepath.Join(s.root, request.ChatID+".json")
	payload := map[string]any{
		"requestId": request.RequestID,
		"chatId":    request.ChatID,
		"chatName":  chatDetail.ChatName,
		"items":     []api.RememberItemResponse{item},
		"stored":    []api.StoredMemoryResponse{stored},
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
		Accepted:      true,
		Status:        "stored",
		RequestID:     request.RequestID,
		ChatID:        request.ChatID,
		MemoryPath:    memoryPath,
		MemoryRoot:    s.root,
		MemoryCount:   1,
		Detail:        "remember request captured; memory root=" + s.root,
		PromptPreview: preview,
		Items:         []api.RememberItemResponse{item},
		Stored:        []api.StoredMemoryResponse{stored},
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
	return out, nil
}

func (s *SQLiteStore) ReadDetail(agentKey string, id string) (*ToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT ID_, AGENT_KEY_, SUBJECT_KEY_, SUMMARY_, SOURCE_TYPE_, CATEGORY_, IMPORTANCE_, TAGS_,
			EMBEDDING_MODEL_, TS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_,
			CASE WHEN EMBEDDING_ IS NULL THEN 0 ELSE 1 END
		FROM MEMORIES
		WHERE ID_ = ? AND (? = '' OR AGENT_KEY_ = ?)`,
		id, strings.TrimSpace(agentKey), strings.TrimSpace(agentKey),
	)
	record, err := scanToolRecord(row)
	if err == sql.ErrNoRows {
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
		`SELECT ID_, AGENT_KEY_, SUBJECT_KEY_, SUMMARY_, SOURCE_TYPE_, CATEGORY_, IMPORTANCE_, TAGS_,
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
	return records, rows.Err()
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
			m.SOURCE_TYPE_, m.SUMMARY_, m.CATEGORY_, m.IMPORTANCE_, m.TAGS_,
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
		`SELECT m.ID_, m.AGENT_KEY_, m.SUBJECT_KEY_, m.SUMMARY_, m.SOURCE_TYPE_, m.CATEGORY_, m.IMPORTANCE_, m.TAGS_,
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
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, TAGS_,
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
		`SELECT ID_, AGENT_KEY_, SUBJECT_KEY_, SUMMARY_, SOURCE_TYPE_, CATEGORY_, IMPORTANCE_, TAGS_,
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
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, TAGS_,
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
		var accessCount sql.NullInt64
		var lastAccessedAt sql.NullInt64
		var score float64

		err := rows.Scan(
			&item.ID, &ts, &requestID, &chatID,
			&item.AgentKey, &item.SubjectKey, &item.SourceType,
			&item.Summary, &item.Category, &item.Importance, &tags,
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
		if tags.Valid && tags.String != "" {
			item.Tags = strings.Split(tags.String, ",")
		}
		// BM25 returns negative scores (more negative = better match), convert to positive
		results = append(results, scoredItem{item: item, score: math.Abs(score)})
	}
	return results, rows.Err()
}

func scanMemoryRow(rows *sql.Rows) (api.StoredMemoryResponse, error) {
	var item api.StoredMemoryResponse
	var ts int64
	var requestID, chatID, tags sql.NullString
	var accessCount sql.NullInt64
	var lastAccessedAt sql.NullInt64

	err := rows.Scan(
		&item.ID, &ts, &requestID, &chatID,
		&item.AgentKey, &item.SubjectKey, &item.SourceType,
		&item.Summary, &item.Category, &item.Importance, &tags,
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
	if tags.Valid && tags.String != "" {
		item.Tags = strings.Split(tags.String, ",")
	}
	return item, nil
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
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES WHERE ID_ = ?`,
		id,
	)
	var item api.StoredMemoryResponse
	var ts int64
	var requestID, chatID, tags sql.NullString
	var accessCount sql.NullInt64
	var lastAccessedAt sql.NullInt64

	err := row.Scan(
		&item.ID, &ts, &requestID, &chatID,
		&item.AgentKey, &item.SubjectKey, &item.SourceType,
		&item.Summary, &item.Category, &item.Importance, &tags,
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
	if tags.Valid && tags.String != "" {
		item.Tags = strings.Split(tags.String, ",")
	}

	// Update access tracking
	now := time.Now().UnixMilli()
	_, _ = s.db.Exec(
		`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
		now, now, id,
	)
	return &item, nil
}

func (s *SQLiteStore) Write(item api.StoredMemoryResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	tagsStr := strings.Join(item.Tags, ",")

	_, err := s.db.Exec(
		`INSERT INTO MEMORIES (ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, TAGS_, UPDATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			SUMMARY_ = excluded.SUMMARY_,
			CATEGORY_ = excluded.CATEGORY_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			TAGS_ = excluded.TAGS_,
			UPDATED_AT_ = excluded.UPDATED_AT_`,
		item.ID, item.CreatedAt, item.RequestID, item.ChatID,
		item.AgentKey, item.SubjectKey, item.SourceType,
		item.Summary, item.Category, item.Importance, tagsStr, item.UpdatedAt,
	)
	if err != nil {
		return err
	}

	// Dual-write to journal
	if s.dualWriteMD {
		_ = AppendJournal(s.root, item)
	}
	return nil
}

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanToolRecord(scanner sqlScanner) (ToolRecord, error) {
	var record ToolRecord
	var tags sql.NullString
	var embeddingModel sql.NullString
	var lastAccessedAt sql.NullInt64
	var hasEmbedding int

	err := scanner.Scan(
		&record.ID, &record.AgentKey, &record.SubjectKey, &record.Content, &record.SourceType,
		&record.Category, &record.Importance, &tags, &embeddingModel, &record.CreatedAt,
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
	if embeddingModel.Valid {
		value := embeddingModel.String
		record.EmbeddingModel = &value
	}
	record.HasEmbedding = hasEmbedding != 0
	if lastAccessedAt.Valid {
		value := lastAccessedAt.Int64
		record.LastAccessedAt = &value
	}
	return record, nil
}

func scanToolRecordWithScore(scanner sqlScanner) (ToolRecord, float64, error) {
	var record ToolRecord
	var tags sql.NullString
	var embeddingModel sql.NullString
	var lastAccessedAt sql.NullInt64
	var hasEmbedding int
	var score float64

	err := scanner.Scan(
		&record.ID, &record.AgentKey, &record.SubjectKey, &record.Content, &record.SourceType,
		&record.Category, &record.Importance, &tags, &embeddingModel, &record.CreatedAt,
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
	if embeddingModel.Valid {
		value := embeddingModel.String
		record.EmbeddingModel = &value
	}
	record.HasEmbedding = hasEmbedding != 0
	if lastAccessedAt.Valid {
		value := lastAccessedAt.Int64
		record.LastAccessedAt = &value
	}
	return record, score, nil
}

func generateMemoryID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "mem_" + hex.EncodeToString(b)
}
