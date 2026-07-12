package kbase

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacySnapshotIncludesCommittedWALFrames(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "legacy"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Disable automatic checkpointing, truncate any schema-initialization WAL,
	// then write the payload. The snapshot must therefore read committed frames
	// that have not been copied back into kbase.db.
	if _, err := store.db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatalf("disable wal autocheckpoint: %v", err)
	}
	if _, err := store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("truncate initial WAL: %v", err)
	}
	chunk := testLegacyChunk("chunk-wal", "docs/wal.md", 0)
	if err := store.UpsertIndexedFile(testLegacyFile("file-wal", chunk.Path), []chunkRecord{chunk}); err != nil {
		t.Fatalf("UpsertIndexedFile: %v", err)
	}
	walInfo, err := os.Stat(store.DBPath() + "-wal")
	if err != nil {
		t.Fatalf("stat WAL: %v", err)
	}
	if walInfo.Size() <= 32 {
		t.Fatalf("WAL size = %d, want at least one frame", walInfo.Size())
	}

	snapshotPath := filepath.Join(root, "migrations", "snapshot.db")
	if err := store.Snapshot(context.Background(), snapshotPath); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snapshot, err := OpenSnapshotStore(snapshotPath)
	if err != nil {
		t.Fatalf("OpenSnapshotStore: %v", err)
	}
	defer snapshot.Close()
	files, chunks, err := snapshot.Counts()
	if err != nil {
		t.Fatalf("snapshot Counts: %v", err)
	}
	if files != 1 || chunks != 1 {
		t.Fatalf("snapshot counts = files:%d chunks:%d, want 1/1", files, chunks)
	}
	got, err := snapshot.ReadChunk(chunk.ID)
	if err != nil {
		t.Fatalf("snapshot ReadChunk: %v", err)
	}
	if got == nil || got.Content != chunk.Content || !reflect.DeepEqual(got.Embedding, chunk.Embedding) {
		t.Fatalf("snapshot chunk = %#v, want %#v", got, chunk)
	}
}

func TestLegacyIterateChunksStreamsValidatedBatches(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	chunks := make([]chunkRecord, 5)
	for index := range chunks {
		chunks[index] = testLegacyChunk("chunk-"+string(rune('a'+index)), "docs/batches.md", index)
	}
	if err := store.UpsertIndexedFile(testLegacyFile("file-batches", "docs/batches.md"), chunks); err != nil {
		t.Fatalf("UpsertIndexedFile: %v", err)
	}
	var batchSizes []int
	var ids []string
	err = store.IterateChunks(2, 3, func(batch []chunkRecord) error {
		batchSizes = append(batchSizes, len(batch))
		for _, chunk := range batch {
			ids = append(ids, chunk.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("IterateChunks: %v", err)
	}
	if !reflect.DeepEqual(batchSizes, []int{2, 2, 1}) {
		t.Fatalf("batch sizes = %v, want [2 2 1]", batchSizes)
	}
	wantIDs := []string{"chunk-a", "chunk-b", "chunk-c", "chunk-d", "chunk-e"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("chunk ids = %v, want %v", ids, wantIDs)
	}
}

func TestLegacyIterateChunksRejectsInvalidMigrationData(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Store, *chunkRecord)
		wantText string
	}{
		{
			name: "dimension",
			mutate: func(_ *Store, chunk *chunkRecord) {
				chunk.Embedding = []float64{1, 2}
				chunk.EmbeddingDimension = 2
			},
			wantText: "embedding dimension mismatch",
		},
		{
			name: "nan",
			mutate: func(_ *Store, chunk *chunkRecord) {
				chunk.Embedding[1] = math.NaN()
			},
			wantText: "NaN or Inf",
		},
		{
			name: "infinity",
			mutate: func(_ *Store, chunk *chunkRecord) {
				chunk.Embedding[1] = math.Inf(1)
			},
			wantText: "NaN or Inf",
		},
		{
			name: "float32 overflow",
			mutate: func(_ *Store, chunk *chunkRecord) {
				chunk.Embedding[1] = math.MaxFloat64
			},
			wantText: "overflows float32",
		},
		{
			name: "content hash",
			mutate: func(_ *Store, chunk *chunkRecord) {
				chunk.ContentHash = "not-the-content-hash"
			},
			wantText: "content hash mismatch",
		},
		{
			name: "malformed blob length",
			mutate: func(store *Store, chunk *chunkRecord) {
				if _, err := store.db.Exec(`UPDATE KBASE_CHUNKS SET EMBEDDING_=? WHERE ID_=?`, []byte{1, 2, 3}, chunk.ID); err != nil {
					t.Fatalf("corrupt embedding BLOB: %v", err)
				}
			},
			wantText: "invalid vector blob length",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			defer store.Close()
			chunk := testLegacyChunk("bad-chunk", "docs/bad.md", 0)
			// Mutations that need SQL run after the initially-valid row exists;
			// in-memory validation mutations run before insertion.
			if test.name != "malformed blob length" {
				test.mutate(store, &chunk)
			}
			if err := store.UpsertIndexedFile(testLegacyFile("bad-file", chunk.Path), []chunkRecord{chunk}); err != nil {
				t.Fatalf("UpsertIndexedFile: %v", err)
			}
			if test.name == "malformed blob length" {
				test.mutate(store, &chunk)
			}
			callbackCalled := false
			err = store.IterateChunks(2, 3, func([]chunkRecord) error {
				callbackCalled = true
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("IterateChunks error = %v, want containing %q", err, test.wantText)
			}
			if !isLegacyDataError(err) {
				t.Fatalf("IterateChunks error type = %T, want legacyDataError for cold rebuild", err)
			}
			if callbackCalled {
				t.Fatal("callback called for invalid first row")
			}
		})
	}
}

func testLegacyFile(id, path string) fileRecord {
	return fileRecord{
		ID: id, Path: path, Ext: ".md", Mime: "text/markdown", Size: 10, MTimeMS: 100,
		SHA256: "file-hash", TextSHA256: "text-hash", Extractor: "text", Status: "active", IndexedAt: 200,
	}
}

func TestMigrationResumeStopsBeforeWorkspaceReconciliation(t *testing.T) {
	for _, test := range []struct {
		name      string
		migration Migration
		want      bool
	}{
		{name: "importing", migration: Migration{State: MigrationImporting}, want: true},
		{name: "legacy retry record", migration: Migration{State: MigrationFailedRetryable}, want: true},
		{name: "failed while importing", migration: Migration{State: MigrationFailedRetryable, LastStage: MigrationImporting}, want: true},
		{name: "reconciling", migration: Migration{State: MigrationIndexing}, want: false},
		{name: "failed after reconciliation", migration: Migration{State: MigrationFailedRetryable, LastStage: MigrationValidating}, want: false},
		{name: "shadowing", migration: Migration{State: MigrationShadowing}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := migrationImportCanResume(&test.migration); got != test.want {
				t.Fatalf("migrationImportCanResume(%#v)=%t, want %t", test.migration, got, test.want)
			}
		})
	}
}

func testLegacyChunk(id, path string, ordinal int) chunkRecord {
	content := "content for " + id
	return chunkRecord{
		ID: id, FileID: "file", Path: path, Ordinal: ordinal, StartLine: ordinal + 1, EndLine: ordinal + 1,
		SourceType: "text", Content: content, ContentHash: shaHex([]byte(content)),
		Embedding: []float64{1, 0.5, -0.25}, EmbeddingModel: "embedding-model", EmbeddingDimension: 3, UpdatedAt: 300,
	}
}
