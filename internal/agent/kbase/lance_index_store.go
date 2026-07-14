package kbase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// workspaceIndexStore is the narrow Lance/control-store mutation surface
// consumed by the extractor and chunker.
type workspaceIndexStore interface {
	Meta(string) string
	SetMeta(string, string) error
	ClearIndex() error
	File(string) (*fileRecord, error)
	ActiveFilePaths() (map[string]struct{}, error)
	UpsertSkippedFile(fileRecord) error
	UpsertIndexedFile(fileRecord, []chunkRecord) error
	MarkDeleted(string) error
}

type lanceIndexStore struct {
	ctx          context.Context
	control      *ControlStore
	retrieval    RetrievalStore
	generationID string
}

func newLanceIndexStore(ctx context.Context, control *ControlStore, retrieval RetrievalStore, generationID string) *lanceIndexStore {
	if ctx == nil {
		ctx = context.Background()
	}
	return &lanceIndexStore{ctx: ctx, control: control, retrieval: retrieval, generationID: generationID}
}

func (s *lanceIndexStore) metaKey(key string) string {
	return "generation:" + s.generationID + ":" + strings.TrimSpace(key)
}

func (s *lanceIndexStore) Meta(key string) string {
	value, err := s.control.Meta(s.ctx, s.metaKey(key))
	if err != nil {
		return ""
	}
	return value
}

func (s *lanceIndexStore) SetMeta(key, value string) error {
	return s.control.SetMeta(s.ctx, s.metaKey(key), value)
}

func (s *lanceIndexStore) ClearIndex() error {
	files, err := s.control.Files(s.ctx, s.generationID)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := s.delete(file, "cleared"); err != nil {
			return err
		}
	}
	return nil
}

func (s *lanceIndexStore) File(path string) (*fileRecord, error) {
	return s.control.File(s.ctx, s.generationID, path)
}

func (s *lanceIndexStore) ActiveFilePaths() (map[string]struct{}, error) {
	return s.control.ActiveFilePaths(s.ctx, s.generationID)
}

func (s *lanceIndexStore) UpsertSkippedFile(rec fileRecord) error {
	rec.ChunkCount = 0
	rec.ChunkSetHash = ""
	return s.replaceOrDelete(rec, nil, FileOperationDelete)
}

func (s *lanceIndexStore) UpsertIndexedFile(rec fileRecord, chunks []chunkRecord) error {
	rec.Status = "active"
	rec.SkipReason = ""
	rec.Error = ""
	rec.ChunkCount = len(chunks)
	for index := range chunks {
		chunks[index].FileID = rec.ID
	}
	rec.ChunkSetHash = chunkValidationSetHash(chunks)
	return s.replaceOrDelete(rec, chunks, FileOperationReplace)
}

func (s *lanceIndexStore) MarkDeleted(path string) error {
	rec, err := s.File(path)
	if err != nil || rec == nil {
		return err
	}
	return s.delete(*rec, "deleted")
}

func (s *lanceIndexStore) delete(rec fileRecord, status string) error {
	rec.Status = status
	if status == "cleared" {
		rec.Status = "deleted"
	}
	rec.ChunkCount = 0
	rec.ChunkSetHash = ""
	rec.IndexedAt = time.Now().UnixMilli()
	return s.replaceOrDelete(rec, nil, FileOperationDelete)
}

func (s *lanceIndexStore) replaceOrDelete(rec fileRecord, chunks []chunkRecord, operation string) error {
	if s == nil || s.control == nil || s.retrieval == nil || strings.TrimSpace(s.generationID) == "" {
		return fmt.Errorf("kbase lance index store is not configured")
	}
	recordJSON, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode KBASE file operation record: %w", err)
	}
	op := FileOperation{
		ID:                 fmt.Sprintf("kbfo_%d", time.Now().UnixNano()),
		GenerationID:       s.generationID,
		FileID:             rec.ID,
		Path:               rec.Path,
		Operation:          operation,
		DesiredContentHash: rec.ChunkSetHash,
		DesiredRecordJSON:  string(recordJSON),
		State:              FileOperationPrepared,
	}
	if pending, pendingErr := s.control.PendingFileOperations(s.ctx, s.generationID); pendingErr == nil {
		reusePendingFileOperation(&op, pending)
	}
	if err := s.control.BeginFileOperation(s.ctx, op); err != nil {
		return err
	}
	var version uint64
	if operation == FileOperationReplace {
		version, err = s.retrieval.ReplaceFileChunks(s.ctx, s.generationID, rec.ID, chunks)
	} else {
		version, err = s.retrieval.DeleteFileChunks(s.ctx, s.generationID, rec.ID)
	}
	if err != nil {
		_ = s.control.failFileOperation(s.ctx, op.ID, err)
		return err
	}
	if err := s.control.MarkFileOperationLanceCommitted(s.ctx, op.ID, version); err != nil {
		_ = s.control.failFileOperation(s.ctx, op.ID, err)
		return err
	}
	if err := s.control.completeFileOperationWithRecord(s.ctx, op.ID, s.generationID, rec); err != nil {
		_ = s.control.failFileOperation(s.ctx, op.ID, err)
		return err
	}
	return nil
}

func reusePendingFileOperation(operation *FileOperation, pending []FileOperation) {
	if operation == nil {
		return
	}
	for _, previous := range pending {
		if previous.Path == operation.Path && previous.Operation == operation.Operation &&
			previous.DesiredContentHash == operation.DesiredContentHash {
			operation.ID = previous.ID
			operation.RetryCount = previous.RetryCount
			return
		}
	}
}

func chunkValidationSetHash(chunks []chunkRecord) string {
	items := make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		item := make([]byte, 0, 256)
		for _, value := range []string{
			chunk.ID, chunk.FileID, chunk.Path, chunk.Heading, chunk.SourceType,
			chunk.LocatorJSON, chunk.ContentHash,
		} {
			item = binary.BigEndian.AppendUint64(item, uint64(len([]byte(value))))
			item = append(item, value...)
		}
		for _, value := range []int{
			chunk.Ordinal, chunk.StartLine, chunk.EndLine, chunk.PageStart,
			chunk.PageEnd, chunk.SlideStart, chunk.SlideEnd,
		} {
			item = binary.BigEndian.AppendUint64(item, uint64(int64(value)))
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return bytes.Compare(items[i], items[j]) < 0 })
	hash := sha256.New()
	for _, item := range items {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(item)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(item)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

var _ workspaceIndexStore = (*lanceIndexStore)(nil)
