package kbase

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/sqlitecontract"

	_ "modernc.org/sqlite"
)

type ControlStore struct {
	root   string
	dbPath string
	db     *sql.DB
}

func OpenControlStore(root string) (*ControlStore, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil, fmt.Errorf("kbase control store root is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(root, "control.db")
	db, err := sql.Open("sqlite", kbaseSQLiteDSN(dbPath, sqliteOpenWrite))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &ControlStore{root: root, dbPath: dbPath, db: db}
	if err := store.initDB(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func OpenReadControlStore(root string) (*ControlStore, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil, fmt.Errorf("kbase control store root is empty")
	}
	dbPath := filepath.Join(root, "control.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", kbaseSQLiteDSN(dbPath, sqliteOpenRead))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &ControlStore{root: root, dbPath: dbPath, db: db}
	if err := sqlitecontract.Verify(db, dbPath, root, kbaseControlSchemaSpec); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.verifySchemaVersion(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *ControlStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *ControlStore) initDB(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS KBASE_META (
			KEY_ TEXT PRIMARY KEY,
			VALUE_ TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS KBASE_FILES (
			GENERATION_ID_ TEXT NOT NULL,
			ID_ TEXT NOT NULL,
			PATH_ TEXT NOT NULL,
			EXT_ TEXT NOT NULL,
			MIME_ TEXT NOT NULL DEFAULT '',
			SIZE_ INTEGER NOT NULL,
			MTIME_MS_ INTEGER NOT NULL,
			SHA256_ TEXT NOT NULL,
			TEXT_SHA256_ TEXT NOT NULL DEFAULT '',
			EXTRACTOR_ TEXT NOT NULL DEFAULT '',
			METADATA_JSON_ TEXT NOT NULL DEFAULT '',
			STATUS_ TEXT NOT NULL,
			SKIP_REASON_ TEXT NOT NULL DEFAULT '',
			ERROR_ TEXT NOT NULL DEFAULT '',
			CHUNK_COUNT_ INTEGER NOT NULL DEFAULT 0,
			CHUNK_SET_HASH_ TEXT NOT NULL DEFAULT '',
			INDEXED_AT_ INTEGER NOT NULL,
			DELETED_AT_ INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(GENERATION_ID_, PATH_)
		)`,
		`CREATE INDEX IF NOT EXISTS IDX_KBASE_CONTROL_FILES_ID ON KBASE_FILES(GENERATION_ID_, ID_)`,
		`CREATE INDEX IF NOT EXISTS IDX_KBASE_CONTROL_FILES_STATUS ON KBASE_FILES(GENERATION_ID_, STATUS_)`,
		`CREATE TABLE IF NOT EXISTS KBASE_GENERATIONS (
			ID_ TEXT PRIMARY KEY,
			AGENT_KEY_ TEXT NOT NULL,
			STATE_ TEXT NOT NULL,
			WORKSPACE_ROOT_ TEXT NOT NULL,
			STORAGE_DIR_ TEXT NOT NULL,
			EMBEDDING_MODEL_KEY_ TEXT NOT NULL DEFAULT '',
			EMBEDDING_PROVIDER_KEY_ TEXT NOT NULL DEFAULT '',
			EMBEDDING_MODEL_ TEXT NOT NULL DEFAULT '',
			EMBEDDING_DIMENSION_ INTEGER NOT NULL DEFAULT 0,
			FTS_TOKENIZER_ TEXT NOT NULL DEFAULT 'icu',
			INDEX_HASH_ TEXT NOT NULL DEFAULT '',
			TABLE_VERSION_ INTEGER NOT NULL DEFAULT 0,
			FILES_ INTEGER NOT NULL DEFAULT 0,
			CHUNKS_ INTEGER NOT NULL DEFAULT 0,
			CREATED_AT_ INTEGER NOT NULL,
			ACTIVATED_AT_ INTEGER NOT NULL DEFAULT 0,
			RETIRED_AT_ INTEGER NOT NULL DEFAULT 0,
			ERROR_ TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS IDX_KBASE_GENERATIONS_ONE_ACTIVE
			ON KBASE_GENERATIONS(STATE_) WHERE STATE_ = 'active'`,
		`CREATE TABLE IF NOT EXISTS KBASE_FILE_OPS (
			ID_ TEXT PRIMARY KEY,
			GENERATION_ID_ TEXT NOT NULL,
			FILE_ID_ TEXT NOT NULL,
			PATH_ TEXT NOT NULL,
			OPERATION_ TEXT NOT NULL,
			DESIRED_CONTENT_HASH_ TEXT NOT NULL DEFAULT '',
			DESIRED_RECORD_JSON_ TEXT NOT NULL DEFAULT '',
			STATE_ TEXT NOT NULL,
			TABLE_VERSION_ INTEGER NOT NULL DEFAULT 0,
			RETRY_COUNT_ INTEGER NOT NULL DEFAULT 0,
			CREATED_AT_ INTEGER NOT NULL,
			UPDATED_AT_ INTEGER NOT NULL,
			ERROR_ TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS IDX_KBASE_FILE_OPS_PENDING ON KBASE_FILE_OPS(GENERATION_ID_, STATE_)`,
		`CREATE TABLE IF NOT EXISTS KBASE_INDEX_RUNS (
			ID_ TEXT PRIMARY KEY,
			GENERATION_ID_ TEXT NOT NULL DEFAULT '',
			ENGINE_ TEXT NOT NULL DEFAULT 'lancedb',
			MODE_ TEXT NOT NULL,
			SCOPE_ TEXT NOT NULL DEFAULT '',
			STATUS_ TEXT NOT NULL,
			STARTED_AT_ INTEGER NOT NULL,
			FINISHED_AT_ INTEGER NOT NULL DEFAULT 0,
			SCANNED_FILES_ INTEGER NOT NULL DEFAULT 0,
			CANDIDATE_PATHS_ INTEGER NOT NULL DEFAULT 0,
			CHANGED_FILES_ INTEGER NOT NULL DEFAULT 0,
			NEW_FILES_ INTEGER NOT NULL DEFAULT 0,
			MODIFIED_FILES_ INTEGER NOT NULL DEFAULT 0,
			METADATA_ONLY_FILES_ INTEGER NOT NULL DEFAULT 0,
			UNCHANGED_FILES_ INTEGER NOT NULL DEFAULT 0,
			DELETED_FILES_ INTEGER NOT NULL DEFAULT 0,
			INDEXED_CHUNKS_ INTEGER NOT NULL DEFAULT 0,
			EMBEDDED_CHUNKS_ INTEGER NOT NULL DEFAULT 0,
			REUSED_CHUNKS_ INTEGER NOT NULL DEFAULT 0,
			PENDING_CHANGES_ INTEGER NOT NULL DEFAULT 0,
			INDEX_BUILD_DURATION_MS_ INTEGER NOT NULL DEFAULT 0,
			VALIDATION_DURATION_MS_ INTEGER NOT NULL DEFAULT 0,
			ERROR_ TEXT NOT NULL DEFAULT ''
		)`,
	}
	err := sqlitecontract.InitializeOrVerify(s.db, s.dbPath, s.root, kbaseControlSchemaSpec, func() (bool, error) {
		return sqlitecontract.HasResidualData(s.root)
	}, func() error {
		for _, stmt := range statements {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("init kbase control schema: %w", err)
			}
		}
		return s.SetMeta(ctx, "schemaVersion", ControlSchemaVersion)
	})
	if err != nil {
		return err
	}
	return s.verifySchemaVersion(ctx)
}

func (s *ControlStore) verifySchemaVersion(ctx context.Context) error {
	schemaVersion, err := s.Meta(ctx, "schemaVersion")
	if err != nil {
		return err
	}
	if schemaVersion != ControlSchemaVersion {
		return sqlitecontract.Unsupported(s.dbPath, s.root, fmt.Sprintf("expected KBASE_META schemaVersion=%q, got %q", ControlSchemaVersion, schemaVersion))
	}
	return nil
}

var kbaseControlSchemaSpec = sqlitecontract.Spec{
	ApplicationID: 0x41504B42, // APKB
	UserVersion:   1,
	Objects: []sqlitecontract.Object{
		{Type: "table", Name: "KBASE_META"},
		{Type: "table", Name: "KBASE_FILES"},
		{Type: "table", Name: "KBASE_GENERATIONS"},
		{Type: "table", Name: "KBASE_FILE_OPS"},
		{Type: "table", Name: "KBASE_INDEX_RUNS"},
		{Type: "index", Name: "IDX_KBASE_CONTROL_FILES_ID"},
		{Type: "index", Name: "IDX_KBASE_CONTROL_FILES_STATUS"},
		{Type: "index", Name: "IDX_KBASE_GENERATIONS_ONE_ACTIVE"},
		{Type: "index", Name: "IDX_KBASE_FILE_OPS_PENDING"},
	},
}

func (s *ControlStore) Meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT VALUE_ FROM KBASE_META WHERE KEY_ = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *ControlStore) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO KBASE_META(KEY_, VALUE_) VALUES(?, ?)
		ON CONFLICT(KEY_) DO UPDATE SET VALUE_ = excluded.VALUE_`, key, value)
	return err
}

func (s *ControlStore) BeginRun(ctx context.Context, mode, generationID string) (IndexRun, error) {
	now := time.Now().UnixMilli()
	run := IndexRun{ID: fmt.Sprintf("kbir_%d", time.Now().UnixNano()), GenerationID: generationID,
		Engine: "lancedb", Mode: strings.TrimSpace(mode), Status: "running", StartedAt: now}
	if run.Mode == "" {
		run.Mode = "manual"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO KBASE_INDEX_RUNS(ID_, GENERATION_ID_, MODE_, STATUS_, STARTED_AT_)
		VALUES(?, ?, ?, ?, ?)`, run.ID, generationID, run.Mode, run.Status, run.StartedAt)
	return run, err
}

func (s *ControlStore) FinishRun(ctx context.Context, run IndexRun, status, errorText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_INDEX_RUNS SET SCOPE_ = ?, STATUS_ = ?, FINISHED_AT_ = ?,
		SCANNED_FILES_ = ?, CANDIDATE_PATHS_ = ?, CHANGED_FILES_ = ?, NEW_FILES_ = ?, MODIFIED_FILES_ = ?,
		METADATA_ONLY_FILES_ = ?, UNCHANGED_FILES_ = ?, DELETED_FILES_ = ?, INDEXED_CHUNKS_ = ?,
		EMBEDDED_CHUNKS_ = ?, REUSED_CHUNKS_ = ?, PENDING_CHANGES_ = ?,
		INDEX_BUILD_DURATION_MS_ = ?, VALIDATION_DURATION_MS_ = ?, ERROR_ = ? WHERE ID_ = ?`,
		run.Scope, status, time.Now().UnixMilli(), run.ScannedFiles, run.CandidatePaths, run.ChangedFiles,
		run.NewFiles, run.ModifiedFiles, run.MetadataOnlyFiles, run.UnchangedFiles, run.DeletedFiles, run.IndexedChunks,
		run.EmbeddedChunks, run.ReusedChunks, run.PendingChanges,
		run.IndexBuildDurationMS, run.ValidationDurationMS, errorText, run.ID)
	return err
}

func (s *ControlStore) LastRun(ctx context.Context) (*IndexRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT ID_, GENERATION_ID_, ENGINE_, MODE_, SCOPE_, STATUS_, STARTED_AT_, FINISHED_AT_,
		SCANNED_FILES_, CANDIDATE_PATHS_, CHANGED_FILES_, NEW_FILES_, MODIFIED_FILES_, METADATA_ONLY_FILES_,
		UNCHANGED_FILES_, DELETED_FILES_, INDEXED_CHUNKS_, EMBEDDED_CHUNKS_, REUSED_CHUNKS_, PENDING_CHANGES_,
		INDEX_BUILD_DURATION_MS_, VALIDATION_DURATION_MS_, ERROR_
		FROM KBASE_INDEX_RUNS ORDER BY STARTED_AT_ DESC LIMIT 1`)
	var run IndexRun
	err := row.Scan(&run.ID, &run.GenerationID, &run.Engine, &run.Mode, &run.Scope, &run.Status, &run.StartedAt, &run.FinishedAt,
		&run.ScannedFiles, &run.CandidatePaths, &run.ChangedFiles, &run.NewFiles, &run.ModifiedFiles,
		&run.MetadataOnlyFiles, &run.UnchangedFiles, &run.DeletedFiles, &run.IndexedChunks,
		&run.EmbeddedChunks, &run.ReusedChunks, &run.PendingChanges,
		&run.IndexBuildDurationMS, &run.ValidationDurationMS, &run.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &run, err
}

func (s *ControlStore) File(ctx context.Context, generationID, path string) (*fileRecord, error) {
	row := s.db.QueryRowContext(ctx, controlFileSelect+` WHERE GENERATION_ID_ = ? AND PATH_ = ?`, generationID, path)
	rec, err := scanControlFile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

const controlFileSelect = `SELECT ID_, PATH_, EXT_, MIME_, SIZE_, MTIME_MS_, SHA256_, TEXT_SHA256_, EXTRACTOR_, METADATA_JSON_,
	STATUS_, SKIP_REASON_, ERROR_, CHUNK_COUNT_, CHUNK_SET_HASH_, INDEXED_AT_, DELETED_AT_ FROM KBASE_FILES`

func (s *ControlStore) Files(ctx context.Context, generationID string) ([]fileRecord, error) {
	rows, err := s.db.QueryContext(ctx, controlFileSelect+` WHERE GENERATION_ID_ = ? ORDER BY PATH_`, generationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fileRecord
	for rows.Next() {
		rec, err := scanControlFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *ControlStore) TrackedFilePaths(ctx context.Context, generationID string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT PATH_ FROM KBASE_FILES WHERE GENERATION_ID_ = ? AND STATUS_ <> 'deleted'`, generationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out[path] = struct{}{}
	}
	return out, rows.Err()
}

func (s *ControlStore) UpsertFile(ctx context.Context, generationID string, rec fileRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO KBASE_FILES(
		GENERATION_ID_, ID_, PATH_, EXT_, MIME_, SIZE_, MTIME_MS_, SHA256_, TEXT_SHA256_, EXTRACTOR_, METADATA_JSON_,
		STATUS_, SKIP_REASON_, ERROR_, CHUNK_COUNT_, CHUNK_SET_HASH_, INDEXED_AT_, DELETED_AT_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(GENERATION_ID_, PATH_) DO UPDATE SET ID_=excluded.ID_, EXT_=excluded.EXT_, MIME_=excluded.MIME_,
		SIZE_=excluded.SIZE_, MTIME_MS_=excluded.MTIME_MS_, SHA256_=excluded.SHA256_, TEXT_SHA256_=excluded.TEXT_SHA256_,
		EXTRACTOR_=excluded.EXTRACTOR_, METADATA_JSON_=excluded.METADATA_JSON_, STATUS_=excluded.STATUS_,
		SKIP_REASON_=excluded.SKIP_REASON_, ERROR_=excluded.ERROR_, CHUNK_COUNT_=excluded.CHUNK_COUNT_,
		CHUNK_SET_HASH_=excluded.CHUNK_SET_HASH_, INDEXED_AT_=excluded.INDEXED_AT_, DELETED_AT_=excluded.DELETED_AT_`,
		generationID, rec.ID, rec.Path, rec.Ext, rec.Mime, rec.Size, rec.MTimeMS, rec.SHA256, rec.TextSHA256,
		rec.Extractor, rec.Metadata, rec.Status, rec.SkipReason, rec.Error, rec.ChunkCount, rec.ChunkSetHash, rec.IndexedAt, rec.DeletedAt)
	return err
}

func (s *ControlStore) MarkFileDeleted(ctx context.Context, generationID, path string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_FILES SET STATUS_='deleted', CHUNK_COUNT_=0, CHUNK_SET_HASH_='', INDEXED_AT_=?, DELETED_AT_=?
		WHERE GENERATION_ID_=? AND PATH_=?`, now, now, generationID, path)
	return err
}

func (s *ControlStore) FileStats(ctx context.Context, generationID string) (FileStats, error) {
	stats := FileStats{Extractors: map[string]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT STATUS_, COUNT(*) FROM KBASE_FILES WHERE GENERATION_ID_=? GROUP BY STATUS_`, generationID)
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			_ = rows.Close()
			return stats, err
		}
		switch status {
		case "active":
			stats.Active = count
		case "skipped":
			stats.Skipped = count
		case "error":
			stats.Error = count
		case "deleted":
			stats.Deleted = count
		}
	}
	if err := rows.Close(); err != nil {
		return stats, err
	}
	extractors, err := s.db.QueryContext(ctx, `SELECT EXTRACTOR_, COUNT(*) FROM KBASE_FILES
		WHERE GENERATION_ID_=? AND EXTRACTOR_<>'' GROUP BY EXTRACTOR_`, generationID)
	if err != nil {
		return stats, err
	}
	defer extractors.Close()
	for extractors.Next() {
		var name string
		var count int
		if err := extractors.Scan(&name, &count); err != nil {
			return stats, err
		}
		stats.Extractors[name] = count
	}
	if len(stats.Extractors) == 0 {
		stats.Extractors = nil
	}
	return stats, extractors.Err()
}

type controlScanner interface {
	Scan(...any) error
}

func scanControlFile(row controlScanner) (fileRecord, error) {
	var rec fileRecord
	err := row.Scan(&rec.ID, &rec.Path, &rec.Ext, &rec.Mime, &rec.Size, &rec.MTimeMS, &rec.SHA256,
		&rec.TextSHA256, &rec.Extractor, &rec.Metadata, &rec.Status, &rec.SkipReason, &rec.Error,
		&rec.ChunkCount, &rec.ChunkSetHash, &rec.IndexedAt, &rec.DeletedAt)
	return rec, err
}

func (s *ControlStore) CreateGeneration(ctx context.Context, generation Generation) error {
	if generation.CreatedAt == 0 {
		generation.CreatedAt = time.Now().UnixMilli()
	}
	if generation.State == "" {
		generation.State = GenerationBuilding
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO KBASE_GENERATIONS(
		ID_, AGENT_KEY_, STATE_, WORKSPACE_ROOT_, STORAGE_DIR_, EMBEDDING_MODEL_KEY_, EMBEDDING_PROVIDER_KEY_,
		EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, FTS_TOKENIZER_, INDEX_HASH_, TABLE_VERSION_, FILES_, CHUNKS_,
		CREATED_AT_, ACTIVATED_AT_, RETIRED_AT_, ERROR_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		generation.ID, generation.AgentKey, generation.State, generation.WorkspaceRoot, generation.StorageDir,
		generation.EmbeddingModelKey, generation.EmbeddingProviderKey, generation.EmbeddingModel,
		generation.EmbeddingDimension, generation.FTSTokenizer, generation.IndexHash,
		generation.TableVersion, generation.Files, generation.Chunks, generation.CreatedAt, generation.ActivatedAt,
		generation.RetiredAt, generation.Error)
	return err
}

func (s *ControlStore) SetGenerationState(ctx context.Context, id, state, errorText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_GENERATIONS SET STATE_=?, ERROR_=? WHERE ID_=?`, state, errorText, id)
	return err
}

func (s *ControlStore) UpdateGenerationStats(ctx context.Context, id string, files, chunks int, tableVersion uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_GENERATIONS SET FILES_=?, CHUNKS_=?, TABLE_VERSION_=? WHERE ID_=?`,
		files, chunks, tableVersion, id)
	return err
}

func (s *ControlStore) Generation(ctx context.Context, id string) (*Generation, error) {
	return scanGenerationRow(s.db.QueryRowContext(ctx, generationSelect+` WHERE ID_=?`, id))
}

func (s *ControlStore) ActiveGeneration(ctx context.Context) (*Generation, error) {
	return scanGenerationRow(s.db.QueryRowContext(ctx, generationSelect+` WHERE STATE_='active' LIMIT 1`))
}

func (s *ControlStore) PreviousGeneration(ctx context.Context, activeID string) (*Generation, error) {
	return scanGenerationRow(s.db.QueryRowContext(ctx, generationSelect+`
		WHERE ID_<>? AND STATE_ IN ('ready','retired') ORDER BY ACTIVATED_AT_ DESC, CREATED_AT_ DESC LIMIT 1`, activeID))
}

const generationSelect = `SELECT ID_, AGENT_KEY_, STATE_, WORKSPACE_ROOT_, STORAGE_DIR_, EMBEDDING_MODEL_KEY_, EMBEDDING_PROVIDER_KEY_,
	EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, FTS_TOKENIZER_, INDEX_HASH_, TABLE_VERSION_, FILES_, CHUNKS_,
	CREATED_AT_, ACTIVATED_AT_, RETIRED_AT_, ERROR_
	FROM KBASE_GENERATIONS`

func scanGenerationRow(row controlScanner) (*Generation, error) {
	var generation Generation
	err := row.Scan(&generation.ID, &generation.AgentKey, &generation.State, &generation.WorkspaceRoot, &generation.StorageDir,
		&generation.EmbeddingModelKey, &generation.EmbeddingProviderKey, &generation.EmbeddingModel,
		&generation.EmbeddingDimension, &generation.FTSTokenizer, &generation.IndexHash,
		&generation.TableVersion, &generation.Files, &generation.Chunks, &generation.CreatedAt, &generation.ActivatedAt,
		&generation.RetiredAt, &generation.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &generation, nil
}

func (s *ControlStore) ActivateGeneration(ctx context.Context, id string) (err error) {
	return s.ActivateGenerationWithMeta(ctx, id, nil)
}

// ActivateGenerationWithMeta performs the generation pointer/state swap and
// its externally visible metadata update in one SQLite transaction. Callers
// must never observe a retired old generation with a partially-published new
// generation manifest.
func (s *ControlStore) ActivateGenerationWithMeta(ctx context.Context, id string, metadata map[string]string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE KBASE_GENERATIONS SET STATE_='retired', RETIRED_AT_=? WHERE STATE_='active'`, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE KBASE_GENERATIONS SET STATE_='active', ACTIVATED_AT_=?, RETIRED_AT_=0, ERROR_='' WHERE ID_=?`, now, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("kbase generation %s not found", id)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO KBASE_META(KEY_, VALUE_) VALUES('activeGeneration', ?)
		ON CONFLICT(KEY_) DO UPDATE SET VALUE_=excluded.VALUE_`, id); err != nil {
		return err
	}
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		if key == "" || key == "activeGeneration" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO KBASE_META(KEY_, VALUE_) VALUES(?, ?)
			ON CONFLICT(KEY_) DO UPDATE SET VALUE_=excluded.VALUE_`, key, value); err != nil {
			return err
		}
	}
	err = tx.Commit()
	return err
}

func (s *ControlStore) BeginFileOperation(ctx context.Context, op FileOperation) error {
	now := time.Now().UnixMilli()
	if op.CreatedAt == 0 {
		op.CreatedAt = now
	}
	if op.UpdatedAt == 0 {
		op.UpdatedAt = now
	}
	if op.State == "" {
		op.State = FileOperationPrepared
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO KBASE_FILE_OPS(ID_, GENERATION_ID_, FILE_ID_, PATH_, OPERATION_,
		DESIRED_CONTENT_HASH_, DESIRED_RECORD_JSON_, STATE_, TABLE_VERSION_, RETRY_COUNT_, CREATED_AT_, UPDATED_AT_, ERROR_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET OPERATION_=excluded.OPERATION_, DESIRED_CONTENT_HASH_=excluded.DESIRED_CONTENT_HASH_,
		DESIRED_RECORD_JSON_=excluded.DESIRED_RECORD_JSON_, STATE_=excluded.STATE_, TABLE_VERSION_=0,
		UPDATED_AT_=excluded.UPDATED_AT_, ERROR_=excluded.ERROR_`,
		op.ID, op.GenerationID, op.FileID, op.Path, op.Operation, op.DesiredContentHash, op.DesiredRecordJSON, op.State,
		op.TableVersion, op.RetryCount, op.CreatedAt, op.UpdatedAt, op.Error)
	return err
}

func (s *ControlStore) MarkFileOperationLanceCommitted(ctx context.Context, id string, version uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_FILE_OPS SET STATE_=?, TABLE_VERSION_=?, UPDATED_AT_=? WHERE ID_=?`,
		FileOperationLanceCommitted, version, time.Now().UnixMilli(), id)
	return err
}

func (s *ControlStore) CompleteFileOperation(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_FILE_OPS SET STATE_=?, UPDATED_AT_=?, ERROR_='' WHERE ID_=?`,
		FileOperationCompleted, time.Now().UnixMilli(), id)
	return err
}

func (s *ControlStore) failFileOperation(ctx context.Context, id string, operationErr error) error {
	errorText := ""
	if operationErr != nil {
		errorText = operationErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE KBASE_FILE_OPS SET RETRY_COUNT_=RETRY_COUNT_+1, UPDATED_AT_=?, ERROR_=? WHERE ID_=?`,
		time.Now().UnixMilli(), errorText, id)
	return err
}

// preparePendingRecovery forces the normal workspace refresh to revisit files
// whose cross-store operation was interrupted. replace/delete are idempotent,
// and the successful replay completes all pending operations for that path.
func (s *ControlStore) preparePendingRecovery(ctx context.Context, generationID string) error {
	operations, err := s.PendingFileOperations(ctx, generationID)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if operation.RetryCount >= 3 {
			var record fileRecord
			if jsonErr := json.Unmarshal([]byte(operation.DesiredRecordJSON), &record); jsonErr == nil && record.ID == operation.FileID {
				record.Status = "error"
				record.Error = "recovery failed three times: " + operation.Error
				record.ChunkCount = 0
				record.ChunkSetHash = ""
				err = s.completeFileOperationWithRecord(ctx, operation.ID, generationID, record)
			} else {
				_, err = s.db.ExecContext(ctx, `UPDATE KBASE_FILES SET STATUS_='error', ERROR_=?, CHUNK_COUNT_=0, CHUNK_SET_HASH_=''
					WHERE GENERATION_ID_=? AND PATH_=?`, "recovery failed three times: "+operation.Error, generationID, operation.Path)
				if err == nil {
					err = s.CompleteFileOperation(ctx, operation.ID)
				}
			}
		} else {
			_, err = s.db.ExecContext(ctx, `UPDATE KBASE_FILES SET MTIME_MS_=-1 WHERE GENERATION_ID_=? AND PATH_=?`,
				generationID, operation.Path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// completeFileOperationWithRecord makes the SQLite half of a cross-store file
// mutation atomic. Lance has already committed when this method is called.
// Completing every pending operation for the same file also closes operations
// that were replayed after a process crash.
func (s *ControlStore) completeFileOperationWithRecord(ctx context.Context, opID, generationID string, rec fileRecord) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	_, err = tx.ExecContext(ctx, `INSERT INTO KBASE_FILES(
		GENERATION_ID_, ID_, PATH_, EXT_, MIME_, SIZE_, MTIME_MS_, SHA256_, TEXT_SHA256_, EXTRACTOR_, METADATA_JSON_,
		STATUS_, SKIP_REASON_, ERROR_, CHUNK_COUNT_, CHUNK_SET_HASH_, INDEXED_AT_, DELETED_AT_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(GENERATION_ID_, PATH_) DO UPDATE SET ID_=excluded.ID_, EXT_=excluded.EXT_, MIME_=excluded.MIME_,
		SIZE_=excluded.SIZE_, MTIME_MS_=excluded.MTIME_MS_, SHA256_=excluded.SHA256_, TEXT_SHA256_=excluded.TEXT_SHA256_,
		EXTRACTOR_=excluded.EXTRACTOR_, METADATA_JSON_=excluded.METADATA_JSON_, STATUS_=excluded.STATUS_,
		SKIP_REASON_=excluded.SKIP_REASON_, ERROR_=excluded.ERROR_, CHUNK_COUNT_=excluded.CHUNK_COUNT_,
		CHUNK_SET_HASH_=excluded.CHUNK_SET_HASH_, INDEXED_AT_=excluded.INDEXED_AT_, DELETED_AT_=excluded.DELETED_AT_`,
		generationID, rec.ID, rec.Path, rec.Ext, rec.Mime, rec.Size, rec.MTimeMS, rec.SHA256, rec.TextSHA256,
		rec.Extractor, rec.Metadata, rec.Status, rec.SkipReason, rec.Error, rec.ChunkCount, rec.ChunkSetHash, rec.IndexedAt, rec.DeletedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE KBASE_FILE_OPS SET STATE_=?, UPDATED_AT_=?, ERROR_=''
		WHERE GENERATION_ID_=? AND PATH_=? AND STATE_<>'completed'`,
		FileOperationCompleted, time.Now().UnixMilli(), generationID, rec.Path)
	if err != nil {
		return err
	}
	// Keep the explicit operation id in the predicate contract even though the
	// path replay update above normally includes it.
	if _, err = tx.ExecContext(ctx, `UPDATE KBASE_FILE_OPS SET STATE_=?, UPDATED_AT_=?, ERROR_='' WHERE ID_=?`,
		FileOperationCompleted, time.Now().UnixMilli(), opID); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

func (s *ControlStore) PendingFileOperations(ctx context.Context, generationID string) ([]FileOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ID_, GENERATION_ID_, FILE_ID_, PATH_, OPERATION_, DESIRED_CONTENT_HASH_, DESIRED_RECORD_JSON_,
		STATE_, TABLE_VERSION_, RETRY_COUNT_, CREATED_AT_, UPDATED_AT_, ERROR_ FROM KBASE_FILE_OPS
		WHERE GENERATION_ID_=? AND STATE_<>'completed' ORDER BY CREATED_AT_`, generationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileOperation
	for rows.Next() {
		var op FileOperation
		if err := rows.Scan(&op.ID, &op.GenerationID, &op.FileID, &op.Path, &op.Operation, &op.DesiredContentHash, &op.DesiredRecordJSON,
			&op.State, &op.TableVersion, &op.RetryCount, &op.CreatedAt, &op.UpdatedAt, &op.Error); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// PurgeDeletedBefore removes expired tombstones only after every operation for
// the path has completed. Lance payload deletion is handled before a record is
// marked deleted, so this is control-plane garbage collection only.
func (s *ControlStore) PurgeDeletedBefore(ctx context.Context, generationID string, cutoff int64) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM KBASE_FILES
		WHERE GENERATION_ID_=? AND STATUS_='deleted' AND DELETED_AT_>0 AND DELETED_AT_<?
		AND NOT EXISTS (
			SELECT 1 FROM KBASE_FILE_OPS op
			WHERE op.GENERATION_ID_=KBASE_FILES.GENERATION_ID_
			AND op.PATH_=KBASE_FILES.PATH_ AND op.STATE_<>'completed'
		)`, generationID, cutoff)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

var _ MetadataStore = (*ControlStore)(nil)
