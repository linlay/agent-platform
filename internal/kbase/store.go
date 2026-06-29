package kbase

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	root   string
	dbPath string
	db     *sql.DB
}

func OpenStore(root string) (*Store, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return nil, fmt.Errorf("kbase store root is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	store := &Store{
		root:   root,
		dbPath: filepath.Join(root, "kbase.db"),
	}
	db, err := sql.Open("sqlite", store.dbPath)
	if err != nil {
		return nil, err
	}
	store.db = db
	if err := store.initDB(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initDB() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS KBASE_META (
			KEY_ TEXT PRIMARY KEY,
			VALUE_ TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS KBASE_FILES (
			ID_ TEXT PRIMARY KEY,
			PATH_ TEXT NOT NULL UNIQUE,
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
			INDEXED_AT_ INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS KBASE_CHUNKS (
			ID_ TEXT PRIMARY KEY,
			FILE_ID_ TEXT NOT NULL,
			PATH_ TEXT NOT NULL,
			ORDINAL_ INTEGER NOT NULL,
			HEADING_ TEXT NOT NULL DEFAULT '',
			START_LINE_ INTEGER NOT NULL,
			END_LINE_ INTEGER NOT NULL,
			SOURCE_TYPE_ TEXT NOT NULL DEFAULT '',
			PAGE_START_ INTEGER NOT NULL DEFAULT 0,
			PAGE_END_ INTEGER NOT NULL DEFAULT 0,
			SLIDE_START_ INTEGER NOT NULL DEFAULT 0,
			SLIDE_END_ INTEGER NOT NULL DEFAULT 0,
			LOCATOR_JSON_ TEXT NOT NULL DEFAULT '',
			CONTENT_ TEXT NOT NULL,
			CONTENT_HASH_ TEXT NOT NULL,
			EMBEDDING_ BLOB,
			EMBEDDING_MODEL_ TEXT NOT NULL DEFAULT '',
			EMBEDDING_DIMENSION_ INTEGER NOT NULL DEFAULT 0,
			UPDATED_AT_ INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS IDX_KBASE_CHUNKS_FILE ON KBASE_CHUNKS(FILE_ID_)`,
		`CREATE INDEX IF NOT EXISTS IDX_KBASE_CHUNKS_PATH ON KBASE_CHUNKS(PATH_)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS KBASE_CHUNKS_FTS USING fts5(
			PATH_, HEADING_, CONTENT_,
			content=KBASE_CHUNKS,
			content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS KBASE_CHUNKS_AI AFTER INSERT ON KBASE_CHUNKS BEGIN
			INSERT INTO KBASE_CHUNKS_FTS(rowid, PATH_, HEADING_, CONTENT_)
			VALUES (new.rowid, new.PATH_, new.HEADING_, new.CONTENT_);
		END`,
		`CREATE TRIGGER IF NOT EXISTS KBASE_CHUNKS_AU AFTER UPDATE ON KBASE_CHUNKS BEGIN
			INSERT INTO KBASE_CHUNKS_FTS(KBASE_CHUNKS_FTS, rowid, PATH_, HEADING_, CONTENT_)
			VALUES ('delete', old.rowid, old.PATH_, old.HEADING_, old.CONTENT_);
			INSERT INTO KBASE_CHUNKS_FTS(rowid, PATH_, HEADING_, CONTENT_)
			VALUES (new.rowid, new.PATH_, new.HEADING_, new.CONTENT_);
		END`,
		`CREATE TRIGGER IF NOT EXISTS KBASE_CHUNKS_AD AFTER DELETE ON KBASE_CHUNKS BEGIN
			INSERT INTO KBASE_CHUNKS_FTS(KBASE_CHUNKS_FTS, rowid, PATH_, HEADING_, CONTENT_)
			VALUES ('delete', old.rowid, old.PATH_, old.HEADING_, old.CONTENT_);
		END`,
		`CREATE TABLE IF NOT EXISTS KBASE_INDEX_RUNS (
			ID_ TEXT PRIMARY KEY,
			MODE_ TEXT NOT NULL,
			STATUS_ TEXT NOT NULL,
			STARTED_AT_ INTEGER NOT NULL,
			FINISHED_AT_ INTEGER,
			SCANNED_FILES_ INTEGER NOT NULL DEFAULT 0,
			CHANGED_FILES_ INTEGER NOT NULL DEFAULT 0,
			DELETED_FILES_ INTEGER NOT NULL DEFAULT 0,
			INDEXED_CHUNKS_ INTEGER NOT NULL DEFAULT 0,
			ERROR_ TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("init kbase schema: %w", err)
		}
	}
	fileColumns := map[string]string{
		"MIME_":          "TEXT NOT NULL DEFAULT ''",
		"TEXT_SHA256_":   "TEXT NOT NULL DEFAULT ''",
		"EXTRACTOR_":     "TEXT NOT NULL DEFAULT ''",
		"METADATA_JSON_": "TEXT NOT NULL DEFAULT ''",
	}
	for name, definition := range fileColumns {
		if err := s.ensureColumn("KBASE_FILES", name, definition); err != nil {
			return err
		}
	}
	chunkColumns := map[string]string{
		"SOURCE_TYPE_":  "TEXT NOT NULL DEFAULT ''",
		"PAGE_START_":   "INTEGER NOT NULL DEFAULT 0",
		"PAGE_END_":     "INTEGER NOT NULL DEFAULT 0",
		"SLIDE_START_":  "INTEGER NOT NULL DEFAULT 0",
		"SLIDE_END_":    "INTEGER NOT NULL DEFAULT 0",
		"LOCATOR_JSON_": "TEXT NOT NULL DEFAULT ''",
	}
	for name, definition := range chunkColumns {
		if err := s.ensureColumn("KBASE_CHUNKS", name, definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureColumn(table string, column string, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) Meta(key string) string {
	row := s.db.QueryRow(`SELECT VALUE_ FROM KBASE_META WHERE KEY_ = ?`, key)
	var value string
	if err := row.Scan(&value); err != nil {
		return ""
	}
	return value
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO KBASE_META(KEY_, VALUE_) VALUES(?, ?)
		ON CONFLICT(KEY_) DO UPDATE SET VALUE_ = excluded.VALUE_`, key, value)
	return err
}

func (s *Store) ClearIndex() error {
	_, err := s.db.Exec(`DELETE FROM KBASE_CHUNKS`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM KBASE_FILES`)
	return err
}

func (s *Store) BeginRun(mode string) (IndexRun, error) {
	now := time.Now().UnixMilli()
	run := IndexRun{
		ID:        fmt.Sprintf("kbir_%d", time.Now().UnixNano()),
		Mode:      strings.TrimSpace(mode),
		Status:    "running",
		StartedAt: now,
	}
	if run.Mode == "" {
		run.Mode = "manual"
	}
	_, err := s.db.Exec(`INSERT INTO KBASE_INDEX_RUNS(ID_, MODE_, STATUS_, STARTED_AT_)
		VALUES(?, ?, ?, ?)`, run.ID, run.Mode, run.Status, run.StartedAt)
	return run, err
}

func (s *Store) FinishRun(run IndexRun, status string, errText string) error {
	run.Status = status
	run.Error = errText
	run.FinishedAt = time.Now().UnixMilli()
	_, err := s.db.Exec(`UPDATE KBASE_INDEX_RUNS
		SET STATUS_ = ?, FINISHED_AT_ = ?, SCANNED_FILES_ = ?, CHANGED_FILES_ = ?,
			DELETED_FILES_ = ?, INDEXED_CHUNKS_ = ?, ERROR_ = ?
		WHERE ID_ = ?`,
		run.Status, run.FinishedAt, run.ScannedFiles, run.ChangedFiles,
		run.DeletedFiles, run.IndexedChunks, run.Error, run.ID)
	return err
}

func (s *Store) LastRun() *IndexRun {
	rows, err := s.db.Query(`SELECT ID_, MODE_, STATUS_, STARTED_AT_, COALESCE(FINISHED_AT_, 0),
		SCANNED_FILES_, CHANGED_FILES_, DELETED_FILES_, INDEXED_CHUNKS_, ERROR_
		FROM KBASE_INDEX_RUNS ORDER BY STARTED_AT_ DESC LIMIT 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	if !rows.Next() {
		return nil
	}
	run, err := scanIndexRun(rows)
	if err != nil {
		return nil
	}
	return &run
}

func scanIndexRun(rows *sql.Rows) (IndexRun, error) {
	var run IndexRun
	err := rows.Scan(&run.ID, &run.Mode, &run.Status, &run.StartedAt, &run.FinishedAt,
		&run.ScannedFiles, &run.ChangedFiles, &run.DeletedFiles, &run.IndexedChunks, &run.Error)
	return run, err
}

func (s *Store) File(path string) (*fileRecord, error) {
	row := s.db.QueryRow(`SELECT ID_, PATH_, EXT_, MIME_, SIZE_, MTIME_MS_, SHA256_, TEXT_SHA256_, EXTRACTOR_, METADATA_JSON_,
			STATUS_, SKIP_REASON_, ERROR_, CHUNK_COUNT_, INDEXED_AT_
		FROM KBASE_FILES WHERE PATH_ = ?`, path)
	rec := fileRecord{}
	err := row.Scan(&rec.ID, &rec.Path, &rec.Ext, &rec.Mime, &rec.Size, &rec.MTimeMS, &rec.SHA256,
		&rec.TextSHA256, &rec.Extractor, &rec.Metadata, &rec.Status, &rec.SkipReason, &rec.Error,
		&rec.ChunkCount, &rec.IndexedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) ActiveFilePaths() (map[string]struct{}, error) {
	rows, err := s.db.Query(`SELECT PATH_ FROM KBASE_FILES WHERE STATUS_ = 'active'`)
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

func (s *Store) UpsertSkippedFile(rec fileRecord) error {
	_, err := s.db.Exec(`INSERT INTO KBASE_FILES(ID_, PATH_, EXT_, MIME_, SIZE_, MTIME_MS_, SHA256_, TEXT_SHA256_, EXTRACTOR_, METADATA_JSON_,
			STATUS_, SKIP_REASON_, ERROR_, CHUNK_COUNT_, INDEXED_AT_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(PATH_) DO UPDATE SET
			EXT_ = excluded.EXT_, MIME_ = excluded.MIME_, SIZE_ = excluded.SIZE_, MTIME_MS_ = excluded.MTIME_MS_,
			SHA256_ = excluded.SHA256_, TEXT_SHA256_ = excluded.TEXT_SHA256_, EXTRACTOR_ = excluded.EXTRACTOR_,
			METADATA_JSON_ = excluded.METADATA_JSON_, STATUS_ = excluded.STATUS_,
			SKIP_REASON_ = excluded.SKIP_REASON_, ERROR_ = excluded.ERROR_,
			CHUNK_COUNT_ = 0, INDEXED_AT_ = excluded.INDEXED_AT_`,
		rec.ID, rec.Path, rec.Ext, rec.Mime, rec.Size, rec.MTimeMS, rec.SHA256, rec.TextSHA256, rec.Extractor, rec.Metadata,
		rec.Status, rec.SkipReason, rec.Error, rec.IndexedAt)
	if err != nil {
		return err
	}
	return s.DeleteChunksForFile(rec.ID)
}

func (s *Store) UpsertIndexedFile(rec fileRecord, chunks []chunkRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	rec.ChunkCount = len(chunks)
	_, err = tx.Exec(`INSERT INTO KBASE_FILES(ID_, PATH_, EXT_, MIME_, SIZE_, MTIME_MS_, SHA256_, TEXT_SHA256_, EXTRACTOR_, METADATA_JSON_,
			STATUS_, SKIP_REASON_, ERROR_, CHUNK_COUNT_, INDEXED_AT_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', '', '', ?, ?)
		ON CONFLICT(PATH_) DO UPDATE SET
			ID_ = excluded.ID_, EXT_ = excluded.EXT_, MIME_ = excluded.MIME_, SIZE_ = excluded.SIZE_,
			MTIME_MS_ = excluded.MTIME_MS_, SHA256_ = excluded.SHA256_, TEXT_SHA256_ = excluded.TEXT_SHA256_,
			EXTRACTOR_ = excluded.EXTRACTOR_, METADATA_JSON_ = excluded.METADATA_JSON_, STATUS_ = 'active',
			SKIP_REASON_ = '', ERROR_ = '', CHUNK_COUNT_ = excluded.CHUNK_COUNT_,
			INDEXED_AT_ = excluded.INDEXED_AT_`,
		rec.ID, rec.Path, rec.Ext, rec.Mime, rec.Size, rec.MTimeMS, rec.SHA256, rec.TextSHA256, rec.Extractor, rec.Metadata,
		rec.ChunkCount, rec.IndexedAt)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM KBASE_CHUNKS WHERE FILE_ID_ = ?`, rec.ID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO KBASE_CHUNKS(ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
			SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_, CONTENT_, CONTENT_HASH_,
			EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, chunk := range chunks {
		vector, encodeErr := encodeVector(chunk.Embedding)
		if encodeErr != nil {
			err = encodeErr
			return err
		}
		if _, err = stmt.Exec(chunk.ID, rec.ID, chunk.Path, chunk.Ordinal, chunk.Heading, chunk.StartLine,
			chunk.EndLine, chunk.SourceType, chunk.PageStart, chunk.PageEnd, chunk.SlideStart, chunk.SlideEnd,
			chunk.LocatorJSON, chunk.Content, chunk.ContentHash, vector, chunk.EmbeddingModel,
			chunk.EmbeddingDimension, chunk.UpdatedAt); err != nil {
			return err
		}
	}
	err = tx.Commit()
	return err
}

func (s *Store) DeleteChunksForFile(fileID string) error {
	_, err := s.db.Exec(`DELETE FROM KBASE_CHUNKS WHERE FILE_ID_ = ?`, fileID)
	return err
}

func (s *Store) MarkDeleted(path string) error {
	rec, err := s.File(path)
	if err != nil || rec == nil {
		return err
	}
	if err := s.DeleteChunksForFile(rec.ID); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE KBASE_FILES SET STATUS_ = 'deleted', CHUNK_COUNT_ = 0, INDEXED_AT_ = ? WHERE PATH_ = ?`,
		time.Now().UnixMilli(), path)
	return err
}

func (s *Store) Counts() (files int, chunks int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM KBASE_FILES WHERE STATUS_ = 'active'`).Scan(&files); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM KBASE_CHUNKS`).Scan(&chunks)
	return
}

func (s *Store) FileStats() (FileStats, error) {
	stats := FileStats{Extractors: map[string]int{}}
	rows, err := s.db.Query(`SELECT STATUS_, COUNT(*) FROM KBASE_FILES GROUP BY STATUS_`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
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
	if err := rows.Err(); err != nil {
		return stats, err
	}
	extractorRows, err := s.db.Query(`SELECT EXTRACTOR_, COUNT(*) FROM KBASE_FILES WHERE EXTRACTOR_ <> '' GROUP BY EXTRACTOR_`)
	if err != nil {
		return stats, err
	}
	defer extractorRows.Close()
	for extractorRows.Next() {
		var extractor string
		var count int
		if err := extractorRows.Scan(&extractor, &count); err != nil {
			return stats, err
		}
		stats.Extractors[extractor] = count
	}
	if len(stats.Extractors) == 0 {
		stats.Extractors = nil
	}
	return stats, extractorRows.Err()
}

type ftsHit struct {
	Chunk chunkRecord
	Score float64
}

func (s *Store) SearchFTS(query string, limit int) ([]ftsHit, error) {
	matchExpr := ftsMatchExpression(query)
	if matchExpr == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT c.ID_, c.FILE_ID_, c.PATH_, c.ORDINAL_, c.HEADING_, c.START_LINE_, c.END_LINE_,
			c.SOURCE_TYPE_, c.PAGE_START_, c.PAGE_END_, c.SLIDE_START_, c.SLIDE_END_, c.LOCATOR_JSON_,
			c.CONTENT_, c.CONTENT_HASH_, c.EMBEDDING_, c.EMBEDDING_MODEL_, c.EMBEDDING_DIMENSION_, c.UPDATED_AT_,
			bm25(KBASE_CHUNKS_FTS) AS score
		FROM KBASE_CHUNKS_FTS fts
		JOIN KBASE_CHUNKS c ON c.rowid = fts.rowid
		WHERE KBASE_CHUNKS_FTS MATCH ?
		ORDER BY score
		LIMIT ?`, matchExpr, limit)
	if err != nil {
		return s.searchLike(query, limit)
	}
	defer rows.Close()
	out := []ftsHit{}
	for rows.Next() {
		chunk, score, err := scanChunkWithScore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ftsHit{Chunk: chunk, Score: math.Abs(score)})
	}
	normalizeFTSScores(out)
	return out, rows.Err()
}

func (s *Store) searchLike(query string, limit int) ([]ftsHit, error) {
	pattern := "%" + strings.TrimSpace(query) + "%"
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
			SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_,
			CONTENT_, CONTENT_HASH_, EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_, 1.0
		FROM KBASE_CHUNKS
		WHERE PATH_ LIKE ? OR HEADING_ LIKE ? OR CONTENT_ LIKE ?
		LIMIT ?`, pattern, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ftsHit{}
	for rows.Next() {
		chunk, score, err := scanChunkWithScore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ftsHit{Chunk: chunk, Score: score})
	}
	return out, rows.Err()
}

func (s *Store) AllChunksWithEmbeddings() ([]chunkRecord, error) {
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
			SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_,
			CONTENT_, CONTENT_HASH_, EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_
		FROM KBASE_CHUNKS
		WHERE EMBEDDING_ IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chunkRecord{}
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, rows.Err()
}

func (s *Store) ReadChunk(id string) (*chunkRecord, error) {
	row := s.db.QueryRow(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
			SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_,
			CONTENT_, CONTENT_HASH_, EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_
		FROM KBASE_CHUNKS WHERE ID_ = ?`, id)
	chunk, err := scanChunkRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &chunk, nil
}

func (s *Store) ReadPath(path string, offset int, limit int) (*ReadResult, error) {
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
			SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_,
			CONTENT_, CONTENT_HASH_, EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_
		FROM KBASE_CHUNKS WHERE PATH_ = ? ORDER BY ORDINAL_`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parts []string
	var startLine, endLine int
	var pageStart, pageEnd, slideStart, slideEnd int
	var sourceType string
	var first bool = true
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		if chunk.EndLine < offset {
			continue
		}
		if startLine == 0 {
			startLine = chunk.StartLine
		}
		if first {
			startLine = maxInt(chunk.StartLine, offset)
			pageStart = chunk.PageStart
			slideStart = chunk.SlideStart
			sourceType = chunk.SourceType
			first = false
		}
		parts = append(parts, chunk.Content)
		endLine = chunk.EndLine
		if chunk.PageEnd > 0 {
			pageEnd = chunk.PageEnd
		}
		if chunk.SlideEnd > 0 {
			slideEnd = chunk.SlideEnd
		}
		if endLine >= offset+limit-1 {
			break
		}
	}
	if len(parts) == 0 {
		return &ReadResult{Found: false, Path: path}, rows.Err()
	}
	return &ReadResult{
		Found:      true,
		Path:       path,
		StartLine:  startLine,
		EndLine:    endLine,
		PageStart:  pageStart,
		PageEnd:    pageEnd,
		SlideStart: slideStart,
		SlideEnd:   slideEnd,
		SourceType: sourceType,
		Content:    strings.Join(parts, "\n"),
	}, rows.Err()
}

func scanChunkWithScore(rows *sql.Rows) (chunkRecord, float64, error) {
	var score float64
	var embedding []byte
	var chunk chunkRecord
	err := rows.Scan(&chunk.ID, &chunk.FileID, &chunk.Path, &chunk.Ordinal, &chunk.Heading,
		&chunk.StartLine, &chunk.EndLine, &chunk.SourceType, &chunk.PageStart, &chunk.PageEnd,
		&chunk.SlideStart, &chunk.SlideEnd, &chunk.LocatorJSON, &chunk.Content, &chunk.ContentHash, &embedding,
		&chunk.EmbeddingModel, &chunk.EmbeddingDimension, &chunk.UpdatedAt, &score)
	if err != nil {
		return chunk, 0, err
	}
	if len(embedding) > 0 {
		vector, err := decodeVector(embedding)
		if err != nil {
			return chunk, 0, err
		}
		chunk.Embedding = vector
	}
	return chunk, score, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanChunkRow(row rowScanner) (chunkRecord, error) {
	var embedding []byte
	var chunk chunkRecord
	err := row.Scan(&chunk.ID, &chunk.FileID, &chunk.Path, &chunk.Ordinal, &chunk.Heading,
		&chunk.StartLine, &chunk.EndLine, &chunk.SourceType, &chunk.PageStart, &chunk.PageEnd,
		&chunk.SlideStart, &chunk.SlideEnd, &chunk.LocatorJSON, &chunk.Content, &chunk.ContentHash, &embedding,
		&chunk.EmbeddingModel, &chunk.EmbeddingDimension, &chunk.UpdatedAt)
	if err != nil {
		return chunk, err
	}
	if len(embedding) > 0 {
		vector, err := decodeVector(embedding)
		if err != nil {
			return chunk, err
		}
		chunk.Embedding = vector
	}
	return chunk, nil
}

func scanChunk(rows *sql.Rows) (chunkRecord, error) {
	return scanChunkRow(rows)
}

func ftsMatchExpression(query string) string {
	terms := strings.Fields(query)
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " AND ")
}

func normalizeFTSScores(items []ftsHit) {
	if len(items) == 0 {
		return
	}
	maxScore := 0.0
	for _, item := range items {
		if item.Score > maxScore {
			maxScore = item.Score
		}
	}
	if maxScore <= 0 {
		for i := range items {
			items[i].Score = 1
		}
		return
	}
	for i := range items {
		items[i].Score = items[i].Score / maxScore
	}
}

func sortedSearchHits(hits []SearchHit, limit int) []SearchHit {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].StartLine < hits[j].StartLine
	})
	if limit > 0 && len(hits) > limit {
		return hits[:limit]
	}
	return hits
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
