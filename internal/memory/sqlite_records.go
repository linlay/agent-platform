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
		`SELECT `+toolMemoryColumns("")+`
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
		`SELECT `+toolMemoryColumns("")+`
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

	item, err := s.readStoredMemoryByIDLocked(id, "memory.sqlite.read")
	if err != nil || item == nil {
		return item, err
	}

	// Update access tracking
	now := time.Now().UnixMilli()
	_, _ = s.db.Exec(
		`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
		now, now, id,
	)
	logMemoryRead("read", "", id, true)
	return item, nil
}

func ptrString(value string) *string {
	return &value
}

func (s *SQLiteStore) readStoredMemoryByIDLocked(id, location string) (*api.StoredMemoryResponse, error) {
	row := s.db.QueryRow(
		`SELECT `+storedMemoryColumns("")+`
		FROM MEMORIES WHERE ID_ = ?`,
		id,
	)
	item, err := scanStoredMemory(row, location)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
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
