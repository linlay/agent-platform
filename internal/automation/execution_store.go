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

	"agent-platform/internal/timecontract"

	_ "modernc.org/sqlite"
)

const (
	defaultExecutionDBFileName = "executions.db"
	executionSchemaVersion     = 2
	executionPreviewLimit      = 240
)

var ErrExecutionHistoryUnavailable = fmt.Errorf("automation execution history is unavailable")

type ExecutionStore struct {
	mu         sync.Mutex
	db         *sql.DB
	dbPath     string
	backupPath string
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
		if store.db != nil {
			_ = store.db.Close()
			store.db = nil
		}
		return nil, err
	}
	return store, nil
}

func (s *ExecutionStore) Status() ExecutionHistoryStatus {
	if s == nil || s.db == nil {
		return ExecutionHistoryStatus{State: ExecutionHistoryUnavailable, Message: ErrExecutionHistoryUnavailable.Error()}
	}
	return ExecutionHistoryStatus{Available: true, State: ExecutionHistoryReady}
}

func (s *ExecutionStore) BackupPath() string {
	if s == nil {
		return ""
	}
	return s.backupPath
}

func (s *ExecutionStore) initDB() error {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return err
	}
	s.db = db

	version, err := executionUserVersion(db)
	if err != nil {
		return err
	}
	exists, err := executionTableExists(db)
	if err != nil {
		return err
	}

	switch {
	case !exists && version == 0:
		var objectCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objectCount); err != nil {
			return fmt.Errorf("inspect empty execution database: %w", err)
		}
		if objectCount != 0 {
			return incompatibleExecutionSchemaError(s.dbPath, fmt.Sprintf("database has %d unexpected schema objects", objectCount))
		}
		if err := createLatestExecutionSchema(db); err != nil {
			return err
		}
	case exists && version == executionSchemaVersion:
		// Validated below.
	case exists && version < executionSchemaVersion:
		if err := validateKnownLegacyExecutionSchema(db); err != nil {
			return incompatibleExecutionSchemaError(s.dbPath, err.Error())
		}
		legacyVersion := version
		if legacyVersion == 0 {
			legacyVersion = 1
		}
		upgradedDB, backupPath, err := upgradeLegacyExecutionDatabase(db, s.dbPath, legacyVersion)
		if err != nil {
			return err
		}
		db = upgradedDB
		s.db = upgradedDB
		s.backupPath = backupPath
	default:
		return incompatibleExecutionSchemaError(s.dbPath, fmt.Sprintf("unsupported user_version=%d", version))
	}

	return validateExecutionSchema(db, s.dbPath)
}

func executionUserVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("inspect execution schema version: %w", err)
	}
	return version, nil
}

func executionTableExists(db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='AUTOMATION_EXECUTIONS'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect execution schema: %w", err)
	}
	return count > 0, nil
}

func latestExecutionSchemaStatements() []string {
	return []string{
		`CREATE TABLE AUTOMATION_EXECUTIONS (
			ID_               TEXT PRIMARY KEY NOT NULL,
			AUTOMATION_ID_    TEXT NOT NULL,
			AUTOMATION_NAME_  TEXT NOT NULL DEFAULT '',
			SOURCE_FILE_      TEXT NOT NULL DEFAULT '',
			AGENT_KEY_        TEXT NOT NULL DEFAULT '',
			TEAM_ID_         TEXT NOT NULL DEFAULT '',
			ZONE_ID_         TEXT NOT NULL,
			QUERY_CONTENT_   TEXT NOT NULL DEFAULT '',
			CHAT_ID_         TEXT NOT NULL DEFAULT '',
			RUN_ID_          TEXT NOT NULL DEFAULT '',
			STATUS_          TEXT NOT NULL DEFAULT 'running'
			                 CHECK (STATUS_ IN ('running', 'success', 'failed', 'canceled')),
			FINISH_REASON_   TEXT NOT NULL DEFAULT '',
			RESULT_CONTENT_  TEXT NOT NULL DEFAULT '',
			ERROR_           TEXT NOT NULL DEFAULT '',
			STARTED_AT_      INTEGER NOT NULL,
			RUN_STARTED_AT_  INTEGER,
			COMPLETED_AT_    INTEGER,
			DURATION_MS_     INTEGER,
			CHECK (DURATION_MS_ IS NULL OR DURATION_MS_ >= 0),
			CHECK (
				(STATUS_ = 'running' AND COMPLETED_AT_ IS NULL AND DURATION_MS_ IS NULL)
				OR
				(STATUS_ <> 'running' AND COMPLETED_AT_ IS NOT NULL AND DURATION_MS_ IS NOT NULL)
			)
		)`,
		`CREATE INDEX IDX_EXEC_AUTOMATION_STARTED ON AUTOMATION_EXECUTIONS(AUTOMATION_ID_, STARTED_AT_ DESC, ID_ DESC)`,
		`CREATE INDEX IDX_EXEC_STARTED ON AUTOMATION_EXECUTIONS(STARTED_AT_ DESC)`,
		`CREATE UNIQUE INDEX IDX_EXEC_RUN ON AUTOMATION_EXECUTIONS(RUN_ID_) WHERE RUN_ID_ <> ''`,
	}
}

func createLatestExecutionSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin execution schema init: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range latestExecutionSchemaStatements() {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("init execution schema: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, executionSchemaVersion)); err != nil {
		return fmt.Errorf("set execution schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit execution schema init: %w", err)
	}
	return nil
}

func backupLegacyExecutionDatabase(db *sql.DB, dbPath string, version int) (string, error) {
	backupPath := fmt.Sprintf("%s.backup-v%d-%s.db", dbPath, version, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if _, err := db.Exec(`VACUUM INTO ?`, backupPath); err != nil {
		return "", err
	}
	backup, err := sql.Open("sqlite", backupPath)
	if err != nil {
		return "", err
	}
	defer backup.Close()
	var integrity string
	if err := backup.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return "", fmt.Errorf("backup integrity_check returned %q", integrity)
	}
	return backupPath, nil
}

func upgradeLegacyExecutionDatabase(legacyDB *sql.DB, dbPath string, version int) (*sql.DB, string, error) {
	backupPath, err := backupLegacyExecutionDatabase(legacyDB, dbPath, version)
	if err != nil {
		return nil, "", fmt.Errorf("backup legacy automation execution database: %w", err)
	}

	tempPath, err := createValidatedExecutionDatabaseTemp(dbPath)
	if err != nil {
		return nil, backupPath, fmt.Errorf("create V2 automation execution database after backup %s: %w", backupPath, err)
	}
	tempOwned := true
	defer func() {
		if tempOwned {
			_ = os.Remove(tempPath)
		}
	}()

	if err := legacyDB.Close(); err != nil {
		return nil, backupPath, fmt.Errorf("close legacy automation execution database after backup %s: %w", backupPath, err)
	}

	rollbackPath := fmt.Sprintf("%s.upgrade-old-%s", dbPath, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.Rename(dbPath, rollbackPath); err != nil {
		return nil, backupPath, fmt.Errorf("prepare atomic automation execution database replacement after backup %s: %w", backupPath, err)
	}
	restoreOriginal := func(cause error) error {
		if removeErr := os.Remove(dbPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("%v; remove failed V2 database before restore: %w", cause, removeErr)
		}
		if restoreErr := os.Rename(rollbackPath, dbPath); restoreErr != nil {
			return fmt.Errorf("%v; restore legacy database failed: %w (backup remains at %s)", cause, restoreErr, backupPath)
		}
		return cause
	}
	if err := os.Rename(tempPath, dbPath); err != nil {
		return nil, backupPath, restoreOriginal(fmt.Errorf("activate V2 automation execution database: %w", err))
	}
	tempOwned = false

	upgradedDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, backupPath, restoreOriginal(fmt.Errorf("open activated V2 automation execution database: %w", err))
	}
	if err := validateExecutionSchema(upgradedDB, dbPath); err != nil {
		_ = upgradedDB.Close()
		return nil, backupPath, restoreOriginal(fmt.Errorf("validate activated V2 automation execution database: %w", err))
	}
	if err := validateExecutionIntegrity(upgradedDB); err != nil {
		_ = upgradedDB.Close()
		return nil, backupPath, restoreOriginal(fmt.Errorf("integrity-check activated V2 automation execution database: %w", err))
	}
	// The consistent backup is the durable legacy artifact. Failure to remove
	// the short-lived rollback copy does not invalidate the active V2 database.
	_ = os.Remove(rollbackPath)
	return upgradedDB, backupPath, nil
}

func createValidatedExecutionDatabaseTemp(dbPath string) (string, error) {
	tempFile, err := os.CreateTemp(filepath.Dir(dbPath), ".executions-v2-*.db")
	if err != nil {
		return "", err
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	tempDB, err := sql.Open("sqlite", tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	cleanup := func() {
		_ = tempDB.Close()
		_ = os.Remove(tempPath)
	}
	if err := createLatestExecutionSchema(tempDB); err != nil {
		cleanup()
		return "", err
	}
	if err := validateExecutionSchema(tempDB, tempPath); err != nil {
		cleanup()
		return "", err
	}
	if err := validateExecutionIntegrity(tempDB); err != nil {
		cleanup()
		return "", err
	}
	if err := tempDB.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return tempPath, nil
}

func validateExecutionIntegrity(db *sql.DB) error {
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return fmt.Errorf("integrity_check returned %q", integrity)
	}
	return nil
}

type executionColumn struct {
	Name         string
	Type         string
	NotNull      bool
	PrimaryKey   bool
	HasDefault   bool
	DefaultValue string
}

func readExecutionColumns(db *sql.DB) ([]executionColumn, error) {
	rows, err := db.Query(`PRAGMA table_info(AUTOMATION_EXECUTIONS)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []executionColumn
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, executionColumn{
			Name:         name,
			Type:         strings.ToUpper(strings.TrimSpace(columnType)),
			NotNull:      notNull == 1,
			PrimaryKey:   primaryKey == 1,
			HasDefault:   defaultValue.Valid,
			DefaultValue: strings.TrimSpace(defaultValue.String),
		})
	}
	return columns, rows.Err()
}

func validateKnownLegacyExecutionSchema(db *sql.DB) error {
	columns, err := readExecutionColumns(db)
	if err != nil {
		return err
	}
	withZone := []executionColumn{
		{Name: "ID_", Type: "TEXT", PrimaryKey: true},
		{Name: "AUTOMATION_ID_", Type: "TEXT", NotNull: true},
		{Name: "AUTOMATION_NAME_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "SOURCE_FILE_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "AGENT_KEY_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "TEAM_ID_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "ZONE_ID_", Type: "TEXT", NotNull: true},
		{Name: "STATUS_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "'running'"},
		{Name: "ERROR_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "STARTED_AT_", Type: "INTEGER", NotNull: true},
		{Name: "COMPLETED_AT_", Type: "INTEGER"},
		{Name: "DURATION_MS_", Type: "INTEGER"},
	}
	withoutZone := append([]executionColumn(nil), withZone[:6]...)
	withoutZone = append(withoutZone, withZone[7:]...)
	if !executionColumnsEqual(columns, withZone) && !executionColumnsEqual(columns, withoutZone) {
		return fmt.Errorf("unknown legacy AUTOMATION_EXECUTIONS columns or constraints")
	}
	for name, want := range map[string]executionIndexExpectation{
		"IDX_EXEC_AUTOMATION": {Columns: []string{"AUTOMATION_ID_"}, Descending: []bool{false}},
		"IDX_EXEC_STARTED":    {Columns: []string{"STARTED_AT_"}, Descending: []bool{true}},
	} {
		got, err := executionIndexDefinition(db, name)
		if err != nil {
			return err
		}
		if strings.Join(got.Columns, ",") != strings.Join(want.Columns, ",") ||
			fmt.Sprint(got.Descending) != fmt.Sprint(want.Descending) || got.Unique || got.Partial {
			return fmt.Errorf("legacy index %s=%#v, want %#v", name, got, want)
		}
	}
	if err := validateNamedExecutionIndexSet(db, map[string]struct{}{
		"IDX_EXEC_AUTOMATION": {},
		"IDX_EXEC_STARTED":    {},
	}); err != nil {
		return err
	}
	var extraObjects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		AND NOT (type='table' AND name='AUTOMATION_EXECUTIONS')
		AND NOT (type='index' AND name IN ('IDX_EXEC_AUTOMATION', 'IDX_EXEC_STARTED'))`).Scan(&extraObjects); err != nil {
		return err
	}
	if extraObjects != 0 {
		return fmt.Errorf("legacy database contains %d unexpected schema objects", extraObjects)
	}
	return nil
}

func executionColumnsEqual(columns, expected []executionColumn) bool {
	if len(columns) != len(expected) {
		return false
	}
	for i := range columns {
		if columns[i] != expected[i] {
			return false
		}
	}
	return true
}

func validateExecutionSchema(db *sql.DB, dbPath string) error {
	version, err := executionUserVersion(db)
	if err != nil {
		return err
	}
	if version != executionSchemaVersion {
		return incompatibleExecutionSchemaError(dbPath, fmt.Sprintf("user_version=%d, want %d", version, executionSchemaVersion))
	}
	columns, err := readExecutionColumns(db)
	if err != nil {
		return fmt.Errorf("inspect execution schema: %w", err)
	}
	expected := []executionColumn{
		{Name: "ID_", Type: "TEXT", NotNull: true, PrimaryKey: true},
		{Name: "AUTOMATION_ID_", Type: "TEXT", NotNull: true},
		{Name: "AUTOMATION_NAME_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "SOURCE_FILE_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "AGENT_KEY_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "TEAM_ID_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "ZONE_ID_", Type: "TEXT", NotNull: true},
		{Name: "QUERY_CONTENT_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "CHAT_ID_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "RUN_ID_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "STATUS_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "'running'"},
		{Name: "FINISH_REASON_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "RESULT_CONTENT_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "ERROR_", Type: "TEXT", NotNull: true, HasDefault: true, DefaultValue: "''"},
		{Name: "STARTED_AT_", Type: "INTEGER", NotNull: true},
		{Name: "RUN_STARTED_AT_", Type: "INTEGER"},
		{Name: "COMPLETED_AT_", Type: "INTEGER"},
		{Name: "DURATION_MS_", Type: "INTEGER"},
	}
	if len(columns) != len(expected) {
		return incompatibleExecutionSchemaError(dbPath, fmt.Sprintf("column count=%d, want %d", len(columns), len(expected)))
	}
	for i := range expected {
		if columns[i] != expected[i] {
			return incompatibleExecutionSchemaError(dbPath, fmt.Sprintf("column %d=%#v, want %#v", i, columns[i], expected[i]))
		}
	}
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='AUTOMATION_EXECUTIONS'`).Scan(&tableSQL); err != nil {
		return incompatibleExecutionSchemaError(dbPath, "table SQL is unavailable")
	}
	normalizedTableSQL := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	if !strings.Contains(normalizedTableSQL, "check (status_ in ('running', 'success', 'failed', 'canceled'))") {
		return incompatibleExecutionSchemaError(dbPath, "STATUS_ check constraint is missing")
	}
	if !strings.Contains(normalizedTableSQL, "check (duration_ms_ is null or duration_ms_ >= 0)") {
		return incompatibleExecutionSchemaError(dbPath, "DURATION_MS_ check constraint is missing")
	}
	if !strings.Contains(normalizedTableSQL, "status_ = 'running' and completed_at_ is null and duration_ms_ is null") ||
		!strings.Contains(normalizedTableSQL, "status_ <> 'running' and completed_at_ is not null and duration_ms_ is not null") {
		return incompatibleExecutionSchemaError(dbPath, "terminal lifecycle check constraint is missing")
	}
	for name, want := range map[string]executionIndexExpectation{
		"IDX_EXEC_AUTOMATION_STARTED": {Columns: []string{"AUTOMATION_ID_", "STARTED_AT_", "ID_"}, Descending: []bool{false, true, true}},
		"IDX_EXEC_STARTED":            {Columns: []string{"STARTED_AT_"}, Descending: []bool{true}},
		"IDX_EXEC_RUN":                {Columns: []string{"RUN_ID_"}, Descending: []bool{false}, Unique: true, Partial: true, SQLFragment: "where run_id_ <> ''"},
	} {
		got, err := executionIndexDefinition(db, name)
		if err != nil {
			return incompatibleExecutionSchemaError(dbPath, err.Error())
		}
		if strings.Join(got.Columns, ",") != strings.Join(want.Columns, ",") ||
			fmt.Sprint(got.Descending) != fmt.Sprint(want.Descending) ||
			got.Unique != want.Unique || got.Partial != want.Partial ||
			(want.SQLFragment != "" && !strings.Contains(got.SQL, want.SQLFragment)) {
			return incompatibleExecutionSchemaError(dbPath, fmt.Sprintf("index %s=%#v, want %#v", name, got, want))
		}
	}
	if err := validateExecutionIndexSet(db); err != nil {
		return incompatibleExecutionSchemaError(dbPath, err.Error())
	}
	var extraObjects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		AND NOT (type='table' AND name='AUTOMATION_EXECUTIONS')
		AND NOT (type='index' AND name IN ('IDX_EXEC_AUTOMATION_STARTED', 'IDX_EXEC_STARTED', 'IDX_EXEC_RUN'))`).Scan(&extraObjects); err != nil {
		return incompatibleExecutionSchemaError(dbPath, err.Error())
	}
	if extraObjects != 0 {
		return incompatibleExecutionSchemaError(dbPath, fmt.Sprintf("database contains %d unexpected schema objects", extraObjects))
	}
	return nil
}

type executionIndexExpectation struct {
	Columns     []string
	Descending  []bool
	Unique      bool
	Partial     bool
	SQL         string
	SQLFragment string
}

func executionIndexDefinition(db *sql.DB, name string) (executionIndexExpectation, error) {
	rows, err := db.Query(`PRAGMA index_list('AUTOMATION_EXECUTIONS')`)
	if err != nil {
		return executionIndexExpectation{}, err
	}
	found := false
	definition := executionIndexExpectation{}
	for rows.Next() {
		var seq, unique, partial int
		var indexName, origin string
		if err := rows.Scan(&seq, &indexName, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return executionIndexExpectation{}, err
		}
		if indexName == name {
			found = true
			definition.Unique = unique == 1
			definition.Partial = partial == 1
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return executionIndexExpectation{}, err
	}
	if err := rows.Close(); err != nil {
		return executionIndexExpectation{}, err
	}
	if !found {
		return executionIndexExpectation{}, fmt.Errorf("index %s is missing", name)
	}

	rows, err = db.Query(`PRAGMA index_xinfo(` + strconv.Quote(name) + `)`)
	if err != nil {
		return executionIndexExpectation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq, cid, descending, key int
		var column sql.NullString
		var collation sql.NullString
		if err := rows.Scan(&seq, &cid, &column, &descending, &collation, &key); err != nil {
			return executionIndexExpectation{}, err
		}
		if key == 1 && column.Valid {
			definition.Columns = append(definition.Columns, column.String)
			definition.Descending = append(definition.Descending, descending == 1)
		}
	}
	if err := rows.Err(); err != nil {
		return executionIndexExpectation{}, err
	}
	var indexSQL sql.NullString
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&indexSQL); err != nil {
		return executionIndexExpectation{}, err
	}
	definition.SQL = strings.ToLower(strings.Join(strings.Fields(indexSQL.String), " "))
	return definition, nil
}

func validateExecutionIndexSet(db *sql.DB) error {
	return validateNamedExecutionIndexSet(db, map[string]struct{}{
		"IDX_EXEC_AUTOMATION_STARTED": {},
		"IDX_EXEC_STARTED":            {},
		"IDX_EXEC_RUN":                {},
	})
}

func validateNamedExecutionIndexSet(db *sql.DB, expected map[string]struct{}) error {
	rows, err := db.Query(`PRAGMA index_list('AUTOMATION_EXECUTIONS')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return err
		}
		if strings.HasPrefix(name, "sqlite_autoindex_") {
			continue
		}
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("unexpected index %s", name)
		}
	}
	return rows.Err()
}

func incompatibleExecutionSchemaError(dbPath, reason string) error {
	return fmt.Errorf("automation execution schema is incompatible (%s): %s was left unchanged", reason, dbPath)
}

func (s *ExecutionStore) Upsert(item Execution) error {
	if s == nil || s.db == nil {
		return ErrExecutionHistoryUnavailable
	}
	item = normalizeExecution(item)
	if err := validateExecution(item); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO AUTOMATION_EXECUTIONS (
			ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_, ZONE_ID_,
			QUERY_CONTENT_, CHAT_ID_, RUN_ID_, STATUS_, FINISH_REASON_, RESULT_CONTENT_, ERROR_,
			STARTED_AT_, RUN_STARTED_AT_, COMPLETED_AT_, DURATION_MS_
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			AUTOMATION_NAME_=excluded.AUTOMATION_NAME_,
			SOURCE_FILE_=excluded.SOURCE_FILE_,
			AGENT_KEY_=excluded.AGENT_KEY_,
			TEAM_ID_=excluded.TEAM_ID_,
			ZONE_ID_=excluded.ZONE_ID_,
			QUERY_CONTENT_=excluded.QUERY_CONTENT_,
			CHAT_ID_=CASE WHEN excluded.CHAT_ID_ <> '' THEN excluded.CHAT_ID_ ELSE AUTOMATION_EXECUTIONS.CHAT_ID_ END,
			RUN_ID_=CASE WHEN excluded.RUN_ID_ <> '' THEN excluded.RUN_ID_ ELSE AUTOMATION_EXECUTIONS.RUN_ID_ END,
			RUN_STARTED_AT_=COALESCE(excluded.RUN_STARTED_AT_, AUTOMATION_EXECUTIONS.RUN_STARTED_AT_),
			STATUS_=excluded.STATUS_,
			FINISH_REASON_=excluded.FINISH_REASON_,
			RESULT_CONTENT_=excluded.RESULT_CONTENT_,
			ERROR_=excluded.ERROR_,
			COMPLETED_AT_=excluded.COMPLETED_AT_,
			DURATION_MS_=excluded.DURATION_MS_
		WHERE AUTOMATION_EXECUTIONS.STATUS_ = 'running'`,
		item.ID, item.AutomationID, item.AutomationName, item.SourceFile, item.AgentKey, item.TeamID, item.ZoneID,
		item.QueryContent, item.ChatID, item.RunID, item.Status, item.FinishReason, item.ResultContent, item.Error,
		item.StartedAt, nullableInt64(item.RunStartedAt), nullableInt64(item.CompletedAt), nullableInt64(item.DurationMs),
	)
	return err
}

// Submit lets the synchronous store act as a lightweight recorder in focused
// tests. Production dispatch uses ExecutionHistoryService, whose Submit never
// performs SQLite I/O on the query goroutine.
func (s *ExecutionStore) Submit(item Execution) {
	_ = s.Upsert(item)
}

// RecordStart and RecordComplete remain as compatibility helpers for store and
// API tests; runtime dispatch uses full snapshots through Submit.
func (s *ExecutionStore) RecordStart(automationID, automationName, sourceFile, agentKey, teamID, zoneID string) (string, error) {
	item := Execution{
		ID:             NewExecutionID(),
		AutomationID:   automationID,
		AutomationName: automationName,
		SourceFile:     sourceFile,
		AgentKey:       agentKey,
		TeamID:         teamID,
		ZoneID:         zoneID,
		Status:         ExecutionStatusRunning,
		StartedAt:      time.Now().UnixMilli(),
	}
	if err := s.Upsert(item); err != nil {
		return "", err
	}
	return item.ID, nil
}

func (s *ExecutionStore) RecordComplete(executionID string, execErr error) error {
	item, err := s.GetExecution(executionID)
	if err != nil {
		return err
	}
	if item == nil {
		return sql.ErrNoRows
	}
	completedAt := time.Now().UnixMilli()
	duration := completedAt - item.StartedAt
	if duration < 0 {
		duration = 0
	}
	item.CompletedAt = executionInt64Ptr(completedAt)
	item.DurationMs = executionInt64Ptr(duration)
	if execErr == nil {
		item.Status = ExecutionStatusSuccess
		item.FinishReason = "complete"
		item.Error = ""
	} else {
		item.Status = ExecutionStatusFailed
		item.FinishReason = "error"
		item.Error = execErr.Error()
	}
	return s.Upsert(*item)
}

func normalizeExecution(item Execution) Execution {
	item.ID = strings.TrimSpace(item.ID)
	item.AutomationID = strings.TrimSpace(item.AutomationID)
	item.AutomationName = strings.TrimSpace(item.AutomationName)
	item.SourceFile = strings.TrimSpace(item.SourceFile)
	item.AgentKey = strings.TrimSpace(item.AgentKey)
	item.TeamID = strings.TrimSpace(item.TeamID)
	item.ZoneID = strings.TrimSpace(item.ZoneID)
	item.ChatID = strings.TrimSpace(item.ChatID)
	item.RunID = strings.TrimSpace(item.RunID)
	item.Status = strings.ToLower(strings.TrimSpace(item.Status))
	item.FinishReason = strings.ToLower(strings.TrimSpace(item.FinishReason))
	item.Error = strings.TrimSpace(item.Error)
	return item
}

func validateExecution(item Execution) error {
	if item.ID == "" || item.AutomationID == "" {
		return fmt.Errorf("execution id and automationId are required")
	}
	if item.ZoneID == "" {
		return fmt.Errorf("zoneId is required")
	}
	if _, err := time.LoadLocation(item.ZoneID); err != nil {
		return fmt.Errorf("invalid zoneId %q: %w", item.ZoneID, err)
	}
	if err := timecontract.ValidateEpochMillis(item.StartedAt, "startedAt", "automation.executions.upsert"); err != nil {
		return err
	}
	for name, value := range map[string]*int64{"runStartedAt": item.RunStartedAt, "completedAt": item.CompletedAt} {
		if value != nil {
			if err := timecontract.ValidateEpochMillis(*value, name, "automation.executions.upsert"); err != nil {
				return err
			}
		}
	}
	switch item.Status {
	case ExecutionStatusRunning:
	case ExecutionStatusSuccess, ExecutionStatusFailed, ExecutionStatusCanceled:
		if item.CompletedAt == nil || item.DurationMs == nil {
			return fmt.Errorf("terminal execution requires completedAt and durationMs")
		}
	default:
		return fmt.Errorf("invalid execution status %q", item.Status)
	}
	if item.DurationMs != nil && *item.DurationMs < 0 {
		return fmt.Errorf("durationMs must be non-negative")
	}
	return nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

const executionBriefSelect = `SELECT ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_, ZONE_ID_,
	CHAT_ID_, RUN_ID_, STATUS_, FINISH_REASON_, ERROR_, STARTED_AT_, RUN_STARTED_AT_, COMPLETED_AT_, DURATION_MS_,
	substr(RESULT_CONTENT_, 1, 1000)
	FROM AUTOMATION_EXECUTIONS`

func (s *ExecutionStore) ListByAutomation(automationID string, limit, offset int) ([]Execution, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, ErrExecutionHistoryUnavailable
	}
	automationID = strings.TrimSpace(automationID)
	limit, offset = normalizeExecutionPage(limit, offset)
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM AUTOMATION_EXECUTIONS WHERE AUTOMATION_ID_=?`, automationID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(executionBriefSelect+` WHERE AUTOMATION_ID_=? ORDER BY STARTED_AT_ DESC, ID_ DESC LIMIT ? OFFSET ?`, automationID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanExecutionBriefs(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, rows.Err()
}

func (s *ExecutionStore) LastExecution(automationID string) (*Execution, error) {
	if s == nil || s.db == nil {
		return nil, ErrExecutionHistoryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(executionBriefSelect+` WHERE AUTOMATION_ID_=? ORDER BY STARTED_AT_ DESC, ID_ DESC LIMIT 1`, strings.TrimSpace(automationID))
	item, err := scanExecutionBrief(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *ExecutionStore) GetExecution(executionID string) (*Execution, error) {
	if s == nil || s.db == nil {
		return nil, ErrExecutionHistoryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT ID_, AUTOMATION_ID_, AUTOMATION_NAME_, SOURCE_FILE_, AGENT_KEY_, TEAM_ID_, ZONE_ID_,
		QUERY_CONTENT_, CHAT_ID_, RUN_ID_, STATUS_, FINISH_REASON_, RESULT_CONTENT_, ERROR_,
		STARTED_AT_, RUN_STARTED_AT_, COMPLETED_AT_, DURATION_MS_
		FROM AUTOMATION_EXECUTIONS WHERE ID_=?`, strings.TrimSpace(executionID))
	item, err := scanExecutionFull(row)
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
		return nil, 0, ErrExecutionHistoryUnavailable
	}
	limit, offset = normalizeExecutionPage(limit, offset)
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM AUTOMATION_EXECUTIONS`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(executionBriefSelect+` ORDER BY STARTED_AT_ DESC, ID_ DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanExecutionBriefs(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, rows.Err()
}

func (s *ExecutionStore) ListRunning() ([]Execution, error) {
	if s == nil || s.db == nil {
		return nil, ErrExecutionHistoryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(executionBriefSelect + ` WHERE STATUS_='running' ORDER BY STARTED_AT_ ASC, ID_ ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanExecutionBriefs(rows)
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

func scanExecutionBriefs(rows *sql.Rows) ([]Execution, error) {
	items := []Execution{}
	for rows.Next() {
		item, err := scanExecutionBrief(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanExecutionBrief(scanner executionScanner) (Execution, error) {
	var item Execution
	var runStartedAt, completedAt, durationMs sql.NullInt64
	var preview string
	if err := scanner.Scan(
		&item.ID, &item.AutomationID, &item.AutomationName, &item.SourceFile, &item.AgentKey, &item.TeamID, &item.ZoneID,
		&item.ChatID, &item.RunID, &item.Status, &item.FinishReason, &item.Error, &item.StartedAt,
		&runStartedAt, &completedAt, &durationMs, &preview,
	); err != nil {
		return Execution{}, err
	}
	applyExecutionNullableTimes(&item, runStartedAt, completedAt, durationMs)
	item.ResultPreview = compactExecutionPreview(preview)
	if err := validatePersistedExecution(item); err != nil {
		return Execution{}, err
	}
	return item, nil
}

func scanExecutionFull(scanner executionScanner) (Execution, error) {
	var item Execution
	var runStartedAt, completedAt, durationMs sql.NullInt64
	if err := scanner.Scan(
		&item.ID, &item.AutomationID, &item.AutomationName, &item.SourceFile, &item.AgentKey, &item.TeamID, &item.ZoneID,
		&item.QueryContent, &item.ChatID, &item.RunID, &item.Status, &item.FinishReason, &item.ResultContent, &item.Error,
		&item.StartedAt, &runStartedAt, &completedAt, &durationMs,
	); err != nil {
		return Execution{}, err
	}
	applyExecutionNullableTimes(&item, runStartedAt, completedAt, durationMs)
	item.ResultPreview = compactExecutionPreview(item.ResultContent)
	if err := validatePersistedExecution(item); err != nil {
		return Execution{}, err
	}
	return item, nil
}

func applyExecutionNullableTimes(item *Execution, runStartedAt, completedAt, durationMs sql.NullInt64) {
	if runStartedAt.Valid {
		value := runStartedAt.Int64
		item.RunStartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Int64
		item.CompletedAt = &value
	}
	if durationMs.Valid {
		value := durationMs.Int64
		item.DurationMs = &value
	}
}

func validatePersistedExecution(item Execution) error {
	if _, err := time.LoadLocation(item.ZoneID); err != nil {
		return fmt.Errorf("invalid persisted zoneId %q: %w", item.ZoneID, err)
	}
	if err := timecontract.ValidateEpochMillis(item.StartedAt, "startedAt", "automation.executions"); err != nil {
		return err
	}
	for name, value := range map[string]*int64{"runStartedAt": item.RunStartedAt, "completedAt": item.CompletedAt} {
		if value != nil {
			if err := timecontract.ValidateEpochMillis(*value, name, "automation.executions"); err != nil {
				return err
			}
		}
	}
	return nil
}

func compactExecutionPreview(content string) string {
	compact := strings.Join(strings.Fields(content), " ")
	runes := []rune(compact)
	if len(runes) <= executionPreviewLimit {
		return compact
	}
	return string(runes[:executionPreviewLimit]) + "…"
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

func NewExecutionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "exec_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + hex.EncodeToString(b)
}
