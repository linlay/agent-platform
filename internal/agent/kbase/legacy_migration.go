package kbase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	modernsqlite "modernc.org/sqlite"
)

type legacyDataError struct{ err error }

func (e *legacyDataError) Error() string { return e.err.Error() }
func (e *legacyDataError) Unwrap() error { return e.err }

func legacyInvalid(format string, args ...any) error {
	return &legacyDataError{err: fmt.Errorf(format, args...)}
}

func isLegacyDataError(err error) bool {
	var target *legacyDataError
	return errors.As(err, &target)
}

func (s *Store) DBPath() string {
	if s == nil {
		return ""
	}
	return s.dbPath
}

// Snapshot creates a transactionally consistent online SQLite backup. The
// backup API includes committed WAL content and avoids copying only kbase.db
// while writers use WAL mode.
func (s *Store) Snapshot(ctx context.Context, target string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("kbase store is not open")
	}
	target = filepath.Clean(strings.TrimSpace(target))
	if target == "" || target == "." {
		return fmt.Errorf("kbase snapshot target is empty")
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("kbase snapshot target already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	type backuper interface {
		NewBackup(string) (*modernsqlite.Backup, error)
	}
	err = connection.Raw(func(driverConnection any) error {
		provider, ok := driverConnection.(backuper)
		if !ok {
			return fmt.Errorf("SQLite driver does not expose the online backup API")
		}
		backup, err := provider.NewBackup(target)
		if err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				_ = backup.Finish()
				return err
			}
			more, stepErr := backup.Step(256)
			if stepErr != nil {
				_ = backup.Finish()
				return stepErr
			}
			if !more {
				return backup.Finish()
			}
		}
	})
	if err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("snapshot kbase sqlite: %w", err)
	}
	return nil
}

func (s *Store) AllChunks() ([]chunkRecord, error) {
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
		SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_,
		CONTENT_, CONTENT_HASH_, EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_
		FROM KBASE_CHUNKS ORDER BY PATH_, ORDINAL_`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []chunkRecord
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func OpenSnapshotStore(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil, fmt.Errorf("kbase snapshot path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", kbaseSQLiteDSN(path, sqliteOpenRead))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Store{root: filepath.Dir(path), dbPath: path, db: db}, nil
}

// IterateChunks validates and streams legacy rows in bounded batches so a
// migration never has to materialize every embedding in Go memory.
func (s *Store) IterateChunks(batchSize, dimension int, fn func([]chunkRecord) error) error {
	if batchSize <= 0 {
		batchSize = 512
	}
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
		SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_,
		CONTENT_, CONTENT_HASH_, EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_
		FROM KBASE_CHUNKS ORDER BY PATH_, ORDINAL_`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	batch := make([]chunkRecord, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := fn(batch); err != nil {
			return err
		}
		batch = make([]chunkRecord, 0, batchSize)
		return nil
	}
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return legacyInvalid("decode legacy chunk row: %v", err)
		}
		if _, exists := seen[chunk.ID]; exists {
			return legacyInvalid("duplicate legacy chunk id %s", chunk.ID)
		}
		seen[chunk.ID] = struct{}{}
		if dimension <= 0 || chunk.EmbeddingDimension != dimension || len(chunk.Embedding) != dimension {
			return legacyInvalid("chunk %s embedding dimension mismatch: row=%d vector=%d expected=%d", chunk.ID, chunk.EmbeddingDimension, len(chunk.Embedding), dimension)
		}
		for _, value := range chunk.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return legacyInvalid("chunk %s contains NaN or Inf embedding value", chunk.ID)
			}
			converted := float32(value)
			if math.IsNaN(float64(converted)) || math.IsInf(float64(converted), 0) {
				return legacyInvalid("chunk %s embedding value overflows float32", chunk.ID)
			}
		}
		if want := shaHex([]byte(chunk.Content)); chunk.ContentHash != "" && chunk.ContentHash != want {
			return legacyInvalid("chunk %s content hash mismatch", chunk.ID)
		}
		batch = append(batch, chunk)
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

type legacyValidationQuery struct {
	Text   string
	Vector []float64
}

func (s *Store) SampleValidationQueries(limit int) ([]legacyValidationQuery, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT PATH_, HEADING_, CONTENT_, EMBEDDING_ FROM KBASE_CHUNKS ORDER BY PATH_, ORDINAL_`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	queries := make([]legacyValidationQuery, 0, limit)
	for rows.Next() && len(queries) < limit {
		var path, heading, content string
		var blob []byte
		if err := rows.Scan(&path, &heading, &content, &blob); err != nil {
			return nil, err
		}
		vector, err := decodeVector(blob)
		if err != nil {
			return nil, legacyInvalid("decode validation vector: %v", err)
		}
		for _, candidate := range []string{strings.TrimSpace(heading), strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), sampleContentQuery(content)} {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			key := strings.ToLower(candidate)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			queries = append(queries, legacyValidationQuery{Text: candidate, Vector: append([]float64(nil), vector...)})
			if len(queries) == limit {
				break
			}
		}
	}
	return queries, rows.Err()
}

func (s *Store) IDDigests() (chunkDigest, fileDigest string, err error) {
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_ FROM KBASE_CHUNKS`)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var chunkIDs []string
	fileSet := map[string]struct{}{}
	for rows.Next() {
		var chunkID, fileID string
		if err := rows.Scan(&chunkID, &fileID); err != nil {
			return "", "", err
		}
		chunkIDs = append(chunkIDs, chunkID)
		fileSet[fileID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	fileIDs := make([]string, 0, len(fileSet))
	for id := range fileSet {
		fileIDs = append(fileIDs, id)
	}
	return stableIDDigest(chunkIDs), stableIDDigest(fileIDs), nil
}

// ChunkValidationHashes computes the exact non-vector payload digest used by
// the activation gate without materializing embedding BLOBs.
func (s *Store) ChunkValidationHashes() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
		SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_, CONTENT_HASH_
		FROM KBASE_CHUNKS ORDER BY FILE_ID_, ORDINAL_`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]chunkRecord{}
	for rows.Next() {
		var chunk chunkRecord
		if err := rows.Scan(&chunk.ID, &chunk.FileID, &chunk.Path, &chunk.Ordinal, &chunk.Heading,
			&chunk.StartLine, &chunk.EndLine, &chunk.SourceType, &chunk.PageStart, &chunk.PageEnd,
			&chunk.SlideStart, &chunk.SlideEnd, &chunk.LocatorJSON, &chunk.ContentHash); err != nil {
			return nil, err
		}
		grouped[chunk.FileID] = append(grouped[chunk.FileID], chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hashes := make(map[string]string, len(grouped))
	for fileID, chunks := range grouped {
		hashes[fileID] = chunkValidationSetHash(chunks)
	}
	return hashes, nil
}

func stableIDDigest(ids []string) string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	hash := sha256.New()
	var length [8]byte
	previous := ""
	for index, id := range ids {
		if index > 0 && id == previous {
			continue
		}
		previous = id
		binary.BigEndian.PutUint64(length[:], uint64(len([]byte(id))))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(id))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sampleContentQuery(content string) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) > 48 {
		runes = runes[:48]
	}
	value := strings.Join(strings.Fields(string(runes)), " ")
	if len([]rune(value)) < 2 {
		return ""
	}
	return value
}
