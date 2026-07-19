package kbase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/sqlitecontract"
)

func TestControlStoreActivatesGenerationAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := OpenControlStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenControlStore: %v", err)
	}
	defer store.Close()

	if schema, err := store.Meta(ctx, "schemaVersion"); err != nil || schema != ControlSchemaVersion {
		t.Fatalf("schemaVersion = %q, %v; want %q", schema, err, ControlSchemaVersion)
	}
	if err := sqlitecontract.Verify(store.db, store.dbPath, store.root, kbaseControlSchemaSpec); err != nil {
		t.Fatalf("verify current control schema: %v", err)
	}
	readStore, err := OpenReadControlStore(store.root)
	if err != nil {
		t.Fatalf("OpenReadControlStore: %v", err)
	}
	if err := readStore.Close(); err != nil {
		t.Fatalf("close read control store: %v", err)
	}
	for _, id := range []string{"generation-1", "generation-2"} {
		if err := store.CreateGeneration(ctx, testControlGeneration(id)); err != nil {
			t.Fatalf("CreateGeneration(%s): %v", id, err)
		}
	}
	if err := store.ActivateGeneration(ctx, "generation-1"); err != nil {
		t.Fatalf("ActivateGeneration(generation-1): %v", err)
	}

	// A failed activation must roll back the retirement of the current active
	// generation and leave both the row and meta pointer consistent.
	if err := store.ActivateGenerationWithMeta(ctx, "missing", map[string]string{"indexHash": "must-not-commit"}); err == nil {
		t.Fatal("ActivateGeneration(missing) succeeded, want error")
	}
	assertActiveGeneration(t, ctx, store, "generation-1")
	if value, err := store.Meta(ctx, "indexHash"); err != nil || value != "" {
		t.Fatalf("failed activation leaked metadata value=%q err=%v", value, err)
	}

	if err := store.ActivateGenerationWithMeta(ctx, "generation-2", map[string]string{"indexHash": "hash-2"}); err != nil {
		t.Fatalf("ActivateGeneration(generation-2): %v", err)
	}
	assertActiveGeneration(t, ctx, store, "generation-2")
	if value, err := store.Meta(ctx, "indexHash"); err != nil || value != "hash-2" {
		t.Fatalf("activation metadata value=%q err=%v", value, err)
	}
	retired, err := store.Generation(ctx, "generation-1")
	if err != nil {
		t.Fatalf("Generation(generation-1): %v", err)
	}
	if retired == nil || retired.State != GenerationRetired || retired.RetiredAt == 0 {
		t.Fatalf("previous generation not retired: %#v", retired)
	}
}

func TestControlStoreDoesNotCreateLegacyMigrationTable(t *testing.T) {
	store, err := OpenControlStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenControlStore: %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='KBASE_MIGRATIONS'`).Scan(&count); err != nil {
		t.Fatalf("query control schema: %v", err)
	}
	if count != 0 {
		t.Fatal("control store created deprecated KBASE_MIGRATIONS table")
	}
}

func TestControlStoreRejectsResidualLegacyKBaseDatabase(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "kbase.db")
	legacy := []byte("legacy SQLite artifact must be ignored")
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("write legacy artifact: %v", err)
	}
	if _, err := OpenControlStore(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("OpenControlStore error = %v, want unsupported storage schema", err)
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy artifact: %v", err)
	}
	if string(got) != string(legacy) {
		t.Fatalf("legacy artifact changed while being rejected: %q", got)
	}
}

func TestOpenReadControlStoreRejectsMismatchedMarker(t *testing.T) {
	root := t.TempDir()
	store, err := OpenControlStore(root)
	if err != nil {
		t.Fatalf("OpenControlStore: %v", err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatalf("clear user version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close control store: %v", err)
	}
	if _, err := OpenReadControlStore(root); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("OpenReadControlStore error = %v, want unsupported storage schema", err)
	}
}

func TestValidateStorageContractsRejectsLegacyAndMismatchedStores(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	source := stubAgentSource{agents: map[string]AgentSpec{
		"docs": testKBaseAgent("docs", filepath.Join(root, "workspace"), "runtime"),
	}}
	manager := NewManager(ManagerOptions{RuntimeDir: runtimeRoot}, source, nil)
	if err := manager.ValidateStorageContracts(); err != nil {
		t.Fatalf("validate clean storage: %v", err)
	}

	storageDir := filepath.Join(runtimeRoot, "docs")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy storage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "kbase.db"), []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy kbase db: %v", err)
	}
	if err := manager.ValidateStorageContracts(); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("legacy storage validation error = %v, want unsupported storage schema", err)
	}

	if err := os.Remove(filepath.Join(storageDir, "kbase.db")); err != nil {
		t.Fatalf("remove legacy test file: %v", err)
	}
	store, err := OpenControlStore(storageDir)
	if err != nil {
		t.Fatalf("open current control store: %v", err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatalf("clear current marker: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current control store: %v", err)
	}
	if err := manager.ValidateStorageContracts(); !errors.Is(err, sqlitecontract.ErrUnsupportedSchema) {
		t.Fatalf("mismatched storage validation error = %v, want unsupported storage schema", err)
	}
}

func TestControlStoreFileOperationJournalTransitions(t *testing.T) {
	ctx := context.Background()
	store, err := OpenControlStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenControlStore: %v", err)
	}
	defer store.Close()

	op := FileOperation{
		ID:                 "operation-1",
		GenerationID:       "generation-1",
		FileID:             "file-1",
		Path:               "docs/one.md",
		Operation:          FileOperationReplace,
		DesiredContentHash: "sha256:abc",
	}
	if err := store.BeginFileOperation(ctx, op); err != nil {
		t.Fatalf("BeginFileOperation: %v", err)
	}
	pending := pendingOperations(t, ctx, store, op.GenerationID)
	if len(pending) != 1 || pending[0].State != FileOperationPrepared {
		t.Fatalf("prepared journal = %#v", pending)
	}

	if err := store.MarkFileOperationLanceCommitted(ctx, op.ID, 42); err != nil {
		t.Fatalf("MarkFileOperationLanceCommitted: %v", err)
	}
	pending = pendingOperations(t, ctx, store, op.GenerationID)
	if len(pending) != 1 || pending[0].State != FileOperationLanceCommitted || pending[0].TableVersion != 42 {
		t.Fatalf("committed journal = %#v", pending)
	}

	if err := store.CompleteFileOperation(ctx, op.ID); err != nil {
		t.Fatalf("CompleteFileOperation: %v", err)
	}
	if pending = pendingOperations(t, ctx, store, op.GenerationID); len(pending) != 0 {
		t.Fatalf("completed operation remains pending: %#v", pending)
	}
}

func TestControlStorePurgesOnlyExpiredDeletedFiles(t *testing.T) {
	store, err := OpenControlStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	generationID := "generation-1"
	now := time.Now().UnixMilli()
	for _, record := range []fileRecord{
		{ID: "old", Path: "old.md", Status: "deleted", IndexedAt: now - 20_000, DeletedAt: now - 20_000},
		{ID: "recent", Path: "recent.md", Status: "deleted", IndexedAt: now, DeletedAt: now},
		{ID: "active", Path: "active.md", Status: "active", IndexedAt: now},
	} {
		if err := store.UpsertFile(ctx, generationID, record); err != nil {
			t.Fatal(err)
		}
	}
	purged, err := store.PurgeDeletedBefore(ctx, generationID, now-10_000)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("purged=%d, want 1", purged)
	}
	files, err := store.Files(ctx, generationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "active.md" || files[1].Path != "recent.md" {
		t.Fatalf("remaining files = %#v", files)
	}
}

func TestControlStorePreparesInterruptedOperationsForRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := OpenControlStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenControlStore: %v", err)
	}
	defer store.Close()

	generationID := "generation-1"
	record := fileRecord{
		ID:         "file-1",
		Path:       "docs/recover.md",
		Ext:        ".md",
		MTimeMS:    1234,
		SHA256:     "file-hash",
		Status:     "active",
		ChunkCount: 2,
		IndexedAt:  100,
	}
	if err := store.UpsertFile(ctx, generationID, record); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	op := FileOperation{
		ID:           "operation-recovery",
		GenerationID: generationID,
		FileID:       record.ID,
		Path:         record.Path,
		Operation:    FileOperationReplace,
	}
	if err := store.BeginFileOperation(ctx, op); err != nil {
		t.Fatalf("BeginFileOperation: %v", err)
	}
	if err := store.preparePendingRecovery(ctx, generationID); err != nil {
		t.Fatalf("preparePendingRecovery: %v", err)
	}
	got, err := store.File(ctx, generationID, record.Path)
	if err != nil {
		t.Fatalf("File after recovery preparation: %v", err)
	}
	if got == nil || got.MTimeMS != -1 || got.Status != "active" {
		t.Fatalf("file after first recovery preparation = %#v", got)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if err := store.failFileOperation(ctx, op.ID, errors.New("replay failed")); err != nil {
			t.Fatalf("failFileOperation attempt %d: %v", attempt, err)
		}
	}
	if err := store.preparePendingRecovery(ctx, generationID); err != nil {
		t.Fatalf("preparePendingRecovery after retry limit: %v", err)
	}
	got, err = store.File(ctx, generationID, record.Path)
	if err != nil {
		t.Fatalf("File after retry limit: %v", err)
	}
	if got == nil || got.Status != "error" || got.ChunkCount != 0 {
		t.Fatalf("file after retry limit = %#v", got)
	}
	if got.Error != "recovery failed three times: replay failed" {
		t.Fatalf("file recovery error = %q", got.Error)
	}
	if pending := pendingOperations(t, ctx, store, generationID); len(pending) != 0 {
		t.Fatalf("retry-exhausted operation remains pending: %#v", pending)
	}
}

func TestFileOperationReplayReusesRetryJournal(t *testing.T) {
	operation := FileOperation{ID: "new-attempt", Path: "docs/retry.md", Operation: FileOperationReplace,
		DesiredContentHash: "sha256:target"}
	reusePendingFileOperation(&operation, []FileOperation{{
		ID: "original-attempt", Path: operation.Path, Operation: operation.Operation,
		DesiredContentHash: operation.DesiredContentHash, RetryCount: 2,
	}})
	if operation.ID != "original-attempt" || operation.RetryCount != 2 {
		t.Fatalf("replayed operation did not reuse retry journal: %#v", operation)
	}
}

func TestControlStoreCompletesReplayedOperationsWithFileRecordAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := OpenControlStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenControlStore: %v", err)
	}
	defer store.Close()

	for _, id := range []string{"old-attempt", "replay-attempt"} {
		if err := store.BeginFileOperation(ctx, FileOperation{
			ID:           id,
			GenerationID: "generation-1",
			FileID:       "file-1",
			Path:         "docs/replay.md",
			Operation:    FileOperationReplace,
		}); err != nil {
			t.Fatalf("BeginFileOperation(%s): %v", id, err)
		}
	}
	if err := store.completeFileOperationWithRecord(ctx, "replay-attempt", "generation-1", fileRecord{
		ID: "file-1", Path: "docs/replay.md", Ext: ".md", SHA256: "hash", Status: "active", ChunkCount: 3,
	}); err != nil {
		t.Fatalf("completeFileOperationWithRecord: %v", err)
	}
	if pending := pendingOperations(t, ctx, store, "generation-1"); len(pending) != 0 {
		t.Fatalf("replayed operations remain pending: %#v", pending)
	}
	file, err := store.File(ctx, "generation-1", "docs/replay.md")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if file == nil || file.ChunkCount != 3 || file.Status != "active" {
		t.Fatalf("atomically committed file = %#v", file)
	}
}

func testControlGeneration(id string) Generation {
	return Generation{
		ID:                 id,
		AgentKey:           "docs",
		State:              GenerationReady,
		WorkspaceRoot:      "/workspace",
		StorageDir:         "/storage",
		EmbeddingModel:     "embedding-model",
		EmbeddingDimension: 3,
		IndexHash:          "index-hash",
	}
}

func assertActiveGeneration(t *testing.T, ctx context.Context, store *ControlStore, want string) {
	t.Helper()
	active, err := store.ActiveGeneration(ctx)
	if err != nil {
		t.Fatalf("ActiveGeneration: %v", err)
	}
	if active == nil || active.ID != want || active.State != GenerationActive {
		t.Fatalf("active generation = %#v, want %q", active, want)
	}
	meta, err := store.Meta(ctx, "activeGeneration")
	if err != nil {
		t.Fatalf("activeGeneration meta: %v", err)
	}
	if meta != want {
		t.Fatalf("activeGeneration meta = %q, want %q", meta, want)
	}
}

func pendingOperations(t *testing.T, ctx context.Context, store *ControlStore, generationID string) []FileOperation {
	t.Helper()
	operations, err := store.PendingFileOperations(ctx, generationID)
	if err != nil {
		t.Fatalf("PendingFileOperations: %v", err)
	}
	return operations
}
