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
		`SELECT `+storedMemoryColumns("m")+`,
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
		`SELECT `+toolMemoryColumns("m")+`,
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
		`SELECT `+storedMemoryColumns("")+`,
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
		`SELECT `+toolMemoryColumns("")+`,
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
		`SELECT `+storedMemoryColumns("")+`
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
		item, err := scanStoredMemory(rows, "memory.sqlite.row")
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
		var score float64
		item, err := scanStoredMemory(rows, "memory.sqlite.search", &score)
		if err != nil {
			return nil, err
		}
		// BM25 returns negative scores (more negative = better match), convert to positive
		results = append(results, scoredItem{item: item, score: math.Abs(score)})
	}
	return results, rows.Err()
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
