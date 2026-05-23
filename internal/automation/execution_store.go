package automation

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const defaultExecutionDBFileName = "executions.db"

type ExecutionStore struct {
	mu     sync.Mutex
	db     *sql.DB
	dbPath string
}

func NewExecutionStore(dir, dbFileName string) (*ExecutionStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("execution store dir is required")
	}
	if strings.TrimSpace(dbFileName) == "" {
		dbFileName = defaultExecutionDBFileName
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	store := &ExecutionStore{dbPath: filepath.Join(dir, dbFileName)}
	if err := store.initDB(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ExecutionStore) initDB() error {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return err
	}
	s.db = db

	statements := []string{
		`CREATE TABLE IF NOT EXISTS AUTOMATION_EXECUTIONS (
			ID_            TEXT PRIMARY KEY,
			AUTOMATION_ID_   TEXT NOT NULL,
			AUTOMATION_NAME_ TEXT NOT NULL DEFAULT '',
			SOURCE_FILE_   TEXT NOT NULL DEFAULT '',
			AGENT_KEY_     TEXT NOT NULL DEFAULT '',
			TEAM_ID_       TEXT NOT NULL DEFAULT '',
			STATUS_        TEXT NOT NULL DEFAULT 'running',
			ERROR_         TEXT NOT NULL DEFAULT '',
			STARTED_AT_    INTEGER NOT NULL,
			COMPLETED_AT_  INTEGER,
			DURATION_MS_   INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS IDX_EXEC_AUTOMATION ON AUTOMATION_EXECUTIONS(AUTOMATION_ID_)`,
		`CREATE INDEX IF NOT EXISTS IDX_EXEC_STARTED ON AUTOMATION_EXECUTIONS(STARTED_AT_ DESC)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("init execution schema: %w", err)
		}
	}
	return nil
}

func (s *ExecutionStore) RecordStart(automationID, automationName, sourceFile, agentKey, teamID string) (string, error) {
	if s == nil || s.db == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	executionID := generateExecutionID()
	startedAt := time.Now().UnixMilli()
	_, err := s.db.Exec(`INSERT INTO AUTOMATION_EXECUTIONS (
			ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_, STATUS_, STARTED_AT_
		) VALUES (?, ?, ?, ?, ?, ?, 'running', ?)`,
		executionID,
		strings.TrimSpace(automationID),
		strings.TrimSpace(automationName),
		strings.TrimSpace(sourceFile),
		strings.TrimSpace(agentKey),
		strings.TrimSpace(teamID),
		startedAt,
	)
	if err != nil {
		return "", err
	}
	return executionID, nil
}

func (s *ExecutionStore) RecordComplete(executionID string, execErr error) error {
	if s == nil || s.db == nil {
		return nil
	}
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var startedAt int64
	if err := s.db.QueryRow(`SELECT STARTED_AT_ FROM AUTOMATION_EXECUTIONS WHERE ID_=?`, executionID).Scan(&startedAt); err != nil {
		return err
	}
	completedAt := time.Now().UnixMilli()
	durationMs := completedAt - startedAt
	if durationMs < 0 {
		durationMs = 0
	}
	status := "success"
	errText := ""
	if execErr != nil {
		status = "failed"
		errText = execErr.Error()
	}
	_, err := s.db.Exec(`UPDATE AUTOMATION_EXECUTIONS
		SET STATUS_=?, ERROR_=?, COMPLETED_AT_=?, DURATION_MS_=?
		WHERE ID_=?`, status, errText, completedAt, durationMs, executionID)
	return err
}

func (s *ExecutionStore) ListByAutomation(automationID string, limit, offset int) ([]Execution, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, nil
	}
	automationID = strings.TrimSpace(automationID)
	limit, offset = normalizeExecutionPage(limit, offset)

	s.mu.Lock()
	defer s.mu.Unlock()

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM AUTOMATION_EXECUTIONS WHERE AUTOMATION_ID_=?`, automationID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_,
			STATUS_, ERROR_, STARTED_AT_, COMPLETED_AT_, DURATION_MS_
		FROM AUTOMATION_EXECUTIONS
		WHERE AUTOMATION_ID_=?
		ORDER BY STARTED_AT_ DESC, ID_ DESC
		LIMIT ? OFFSET ?`, automationID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := scanExecutions(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, rows.Err()
}

func (s *ExecutionStore) LastExecution(automationID string) (*Execution, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	automationID = strings.TrimSpace(automationID)

	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(`SELECT ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_,
			STATUS_, ERROR_, STARTED_AT_, COMPLETED_AT_, DURATION_MS_
		FROM AUTOMATION_EXECUTIONS
		WHERE AUTOMATION_ID_=?
		ORDER BY STARTED_AT_ DESC, ID_ DESC
		LIMIT 1`, automationID)
	item, err := scanExecution(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *ExecutionStore) ListRecent(limit, offset int) ([]Execution, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, nil
	}
	limit, offset = normalizeExecutionPage(limit, offset)

	s.mu.Lock()
	defer s.mu.Unlock()

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM AUTOMATION_EXECUTIONS`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_,
			STATUS_, ERROR_, STARTED_AT_, COMPLETED_AT_, DURATION_MS_
		FROM AUTOMATION_EXECUTIONS
		ORDER BY STARTED_AT_ DESC, ID_ DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := scanExecutions(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, rows.Err()
}

func (s *ExecutionStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Close()
	s.db = nil
	return err
}

type executionScanner interface {
	Scan(dest ...any) error
}

func scanExecutions(rows *sql.Rows) ([]Execution, error) {
	items := []Execution{}
	for rows.Next() {
		item, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func scanExecution(scanner executionScanner) (Execution, error) {
	var item Execution
	var completedAt sql.NullInt64
	var durationMs sql.NullInt64
	if err := scanner.Scan(
		&item.ID,
		&item.AutomationID,
		&item.AutomationName,
		&item.SourceFile,
		&item.AgentKey,
		&item.TeamID,
		&item.Status,
		&item.Error,
		&item.StartedAt,
		&completedAt,
		&durationMs,
	); err != nil {
		return Execution{}, err
	}
	if completedAt.Valid {
		item.CompletedAt = &completedAt.Int64
	}
	if durationMs.Valid {
		item.DurationMs = &durationMs.Int64
	}
	return item, nil
}

func normalizeExecutionPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func generateExecutionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "exec_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + hex.EncodeToString(b)
}
