package kbase

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexAndQueryHashesHaveSeparateInputs(t *testing.T) {
	base := hashTestResolvedConfig(t.TempDir())
	indexHash := computeIndexHash(base)
	queryHash := computeQueryHash(base)

	queryOnly := base
	queryOnly.Retrieval.TopK++
	if got := computeIndexHash(queryOnly); got != indexHash {
		t.Fatalf("retrieval-only change altered index hash: %q != %q", got, indexHash)
	}
	if got := computeQueryHash(queryOnly); got == queryHash {
		t.Fatal("retrieval-only change did not alter query hash")
	}

	indexOnly := base
	indexOnly.Chunk.MaxTokens++
	if got := computeIndexHash(indexOnly); got == indexHash {
		t.Fatal("index-affecting change did not alter index hash")
	}
}

func TestControlSchemaVersionDoesNotChangeIndexHash(t *testing.T) {
	cfg := hashTestResolvedConfig(t.TempDir())
	got := computeIndexHash(cfg)
	if want := computeIndexHashForSchema(cfg, IndexSchemaVersion); got != want {
		t.Fatalf("index hash = %q, want %q", got, want)
	}
	if got == computeIndexHashForSchema(cfg, ControlSchemaVersion) {
		t.Fatalf("control schema %s unexpectedly participates in index hash", ControlSchemaVersion)
	}
}

func TestIndexWorkspaceUsesControlGenerationHash(t *testing.T) {
	cfg := hashTestResolvedConfig(t.TempDir())
	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	cfg.IndexHash = computeIndexHash(cfg)
	store := &hashWorkspaceStore{meta: map[string]string{"indexHash": cfg.IndexHash}}
	if err := indexWorkspace(context.Background(), store, cfg, nil, false, &IndexRun{}); err != nil {
		t.Fatalf("index workspace: %v", err)
	}
	if store.clearCalls != 0 {
		t.Fatalf("same Lance index hash cleared index %d time(s)", store.clearCalls)
	}
	if got := store.meta["schemaVersion"]; got != ControlSchemaVersion {
		t.Fatalf("schemaVersion = %q, want %q", got, ControlSchemaVersion)
	}
}

func hashTestResolvedConfig(root string) resolvedConfig {
	return resolvedConfig{
		AgentKey:      "docs",
		WorkspaceRoot: filepath.Join(root, "workspace"),
		StorageDir:    filepath.Join(root, "storage"),
		Storage:       "runtime",
		Embedding:     EmbeddingSnapshot{ModelKey: "embedding-v1", ProviderKey: "provider", Model: "text-embedding-v1", Dimension: 1024, Timeout: 15},
		Include:       DefaultIncludePatterns(),
		Exclude:       DefaultExcludePatterns(),
		Chunk:         DefaultChunkConfig(),
		Retrieval:     DefaultAgentConfig().Retrieval,
		FTSTokenizer:  defaultFTSTokenizer,
		Extraction:    ExtractionConfig{Timeout: time.Minute, MaxFileBytes: 50 * 1024 * 1024},
	}
}

type hashWorkspaceStore struct {
	meta       map[string]string
	clearCalls int
}

func (s *hashWorkspaceStore) Meta(key string) string { return s.meta[key] }
func (s *hashWorkspaceStore) SetMeta(key, value string) error {
	if s.meta == nil {
		s.meta = map[string]string{}
	}
	s.meta[key] = value
	return nil
}
func (s *hashWorkspaceStore) ClearIndex() error                { s.clearCalls++; return nil }
func (s *hashWorkspaceStore) File(string) (*fileRecord, error) { return nil, nil }
func (s *hashWorkspaceStore) TrackedFilePaths() (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (s *hashWorkspaceStore) FileEmbeddings(string, string, int) (map[string][]float64, error) {
	return map[string][]float64{}, nil
}
func (s *hashWorkspaceStore) UpsertMetadataFile(fileRecord) error               { return nil }
func (s *hashWorkspaceStore) UpsertSkippedFile(fileRecord) error                { return nil }
func (s *hashWorkspaceStore) UpsertIndexedFile(fileRecord, []chunkRecord) error { return nil }
func (s *hashWorkspaceStore) MarkDeleted(string) error                          { return nil }

var _ workspaceIndexStore = (*hashWorkspaceStore)(nil)
