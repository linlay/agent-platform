package memory

import (
	"database/sql"
	"math"
	"strings"

	"agent-platform/internal/api"

	_ "modernc.org/sqlite"
)

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
