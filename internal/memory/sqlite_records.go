package memory

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"agent-platform/internal/api"

	_ "modernc.org/sqlite"
)

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
	if err := validateStoredMemoryTimeContract(item, "memory.sqlite.read"); err != nil {
		return nil, err
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

func ptrString(value string) *string {
	return &value
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
	if err := validateStoredMemoryTimeContract(item, "memory.sqlite.projection"); err != nil {
		return nil, err
	}
	normalized := normalizeStoredItem(item)
	return &normalized, nil
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
	if err := validateToolRecordTimeContract(record, "memory.sqlite.toolRecord"); err != nil {
		return record, err
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
	if err := validateToolRecordTimeContract(record, "memory.sqlite.scoredToolRecord"); err != nil {
		return record, 0, err
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

func generateMemoryID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "mem_" + hex.EncodeToString(b)
}

func generateHistoryID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "hist_" + hex.EncodeToString(b)
}
