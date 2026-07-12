package kbase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexAndQueryHashesHaveSeparateInputs(t *testing.T) {
	base := hashTestResolvedConfig(t.TempDir())
	baseIndexHash := computeIndexHash(base)
	baseQueryHash := computeQueryHash(base)

	retrievalCases := map[string]func(*resolvedConfig){
		"top k":                func(cfg *resolvedConfig) { cfg.Retrieval.TopK++ },
		"fusion":               func(cfg *resolvedConfig) { cfg.Retrieval.Fusion = "other" },
		"rrf k":                func(cfg *resolvedConfig) { cfg.Retrieval.RRFK++ },
		"vector weight":        func(cfg *resolvedConfig) { cfg.Retrieval.VectorWeight = 0.6 },
		"fts weight":           func(cfg *resolvedConfig) { cfg.Retrieval.FTSWeight = 0.4 },
		"candidate floor":      func(cfg *resolvedConfig) { cfg.Retrieval.CandidateFloor++ },
		"candidate multiplier": func(cfg *resolvedConfig) { cfg.Retrieval.CandidateMultiplier++ },
		"candidate max":        func(cfg *resolvedConfig) { cfg.Retrieval.CandidateMax++ },
	}
	for name, mutate := range retrievalCases {
		t.Run(name, func(t *testing.T) {
			cfg := cloneHashTestConfig(base)
			mutate(&cfg)
			if got := computeIndexHash(cfg); got != baseIndexHash {
				t.Fatalf("retrieval-only change altered index hash: got %q want %q", got, baseIndexHash)
			}
			if got := computeQueryHash(cfg); got == baseQueryHash {
				t.Fatalf("retrieval-only change did not alter query hash: %q", got)
			}
		})
	}

	indexCases := map[string]func(*resolvedConfig){
		"workspace":           func(cfg *resolvedConfig) { cfg.WorkspaceRoot += "-other" },
		"storage":             func(cfg *resolvedConfig) { cfg.Storage = "workspace" },
		"embedding model key": func(cfg *resolvedConfig) { cfg.Embedding.ModelKey = "embedding-v2" },
		"embedding model":     func(cfg *resolvedConfig) { cfg.Embedding.Model = "text-embedding-v2" },
		"embedding dimension": func(cfg *resolvedConfig) { cfg.Embedding.Dimension++ },
		"include":             func(cfg *resolvedConfig) { cfg.Include = append(cfg.Include, "**/*.rst") },
		"exclude":             func(cfg *resolvedConfig) { cfg.Exclude = append(cfg.Exclude, "private/**") },
		"chunk":               func(cfg *resolvedConfig) { cfg.Chunk.MaxTokens++ },
		"extraction":          func(cfg *resolvedConfig) { cfg.Extraction.PPTX.IncludeNotes = false },
		"fts tokenizer":       func(cfg *resolvedConfig) { cfg.FTSTokenizer = "ngram" },
	}
	for name, mutate := range indexCases {
		t.Run(name, func(t *testing.T) {
			cfg := cloneHashTestConfig(base)
			mutate(&cfg)
			if got := computeIndexHash(cfg); got == baseIndexHash {
				t.Fatalf("index-affecting change did not alter index hash: %q", got)
			}
			if got := computeQueryHash(cfg); got != baseQueryHash {
				t.Fatalf("index-only change altered query hash: got %q want %q", got, baseQueryHash)
			}
		})
	}

	if got := computeConfigHash(base); got != baseIndexHash {
		t.Fatalf("legacy configHash must alias indexHash: got %q want %q", got, baseIndexHash)
	}
}

func TestIndexWorkspaceDerivesLegacyIndexHashWithoutRetrievalRebuild(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	storage := filepath.Join(root, "storage")
	for _, dir := range []string{workspace, storage} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	previous := hashTestResolvedConfig(root)
	previous.WorkspaceRoot = workspace
	previous.StorageDir = storage
	legacy := manifestFromResolved(previous)
	legacy.ConfigHash = "sha256:legacy-config-hash-included-retrieval"
	legacy.IndexHash = ""
	legacy.QueryHash = ""
	writeTestManifest(t, storage, legacy)

	current := cloneHashTestConfig(previous)
	current.Retrieval.VectorWeight = 0.55
	current.Retrieval.FTSWeight = 0.45
	current.ConfigHash = computeConfigHash(current)
	store := &hashWorkspaceStore{meta: map[string]string{"configHash": legacy.ConfigHash}}

	if err := indexWorkspace(context.Background(), store, current, nil, false, &IndexRun{}); err != nil {
		t.Fatalf("index workspace: %v", err)
	}
	if store.clearCalls != 0 {
		t.Fatalf("retrieval-only config change cleared the index %d time(s)", store.clearCalls)
	}
	if got := store.meta["indexHash"]; got != computeIndexHash(current) {
		t.Fatalf("indexHash meta mismatch: got %q want %q", got, computeIndexHash(current))
	}
	if got := store.meta["queryHash"]; got != computeQueryHash(current) {
		t.Fatalf("queryHash meta mismatch: got %q want %q", got, computeQueryHash(current))
	}
	if got := store.meta["configHash"]; got != store.meta["indexHash"] {
		t.Fatalf("legacy configHash must mirror indexHash: config=%q index=%q", got, store.meta["indexHash"])
	}
}

func TestIndexWorkspaceClearsWhenIndexHashChanges(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	storage := filepath.Join(root, "storage")
	for _, dir := range []string{workspace, storage} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	previous := hashTestResolvedConfig(root)
	previous.WorkspaceRoot = workspace
	previous.StorageDir = storage
	legacy := manifestFromResolved(previous)
	legacy.ConfigHash = "sha256:legacy"
	writeTestManifest(t, storage, legacy)

	current := cloneHashTestConfig(previous)
	current.Chunk.MaxTokens++
	current.ConfigHash = computeConfigHash(current)
	store := &hashWorkspaceStore{meta: map[string]string{"configHash": legacy.ConfigHash}}

	if err := indexWorkspace(context.Background(), store, current, nil, false, &IndexRun{}); err != nil {
		t.Fatalf("index workspace: %v", err)
	}
	if store.clearCalls != 1 {
		t.Fatalf("index-affecting config change cleared index %d times, want 1", store.clearCalls)
	}
}

func hashTestResolvedConfig(root string) resolvedConfig {
	return resolvedConfig{
		AgentKey:      "docs",
		WorkspaceRoot: filepath.Join(root, "workspace"),
		StorageDir:    filepath.Join(root, "storage"),
		Storage:       "runtime",
		Embedding: EmbeddingSnapshot{
			ModelKey:     "embedding-v1",
			ProviderKey:  "provider",
			Model:        "text-embedding-v1",
			Dimension:    1024,
			Timeout:      15,
			EndpointPath: "/v1/embeddings",
		},
		Include:      DefaultIncludePatterns(),
		Exclude:      DefaultExcludePatterns(),
		Chunk:        DefaultChunkConfig(),
		Retrieval:    DefaultAgentConfig().Retrieval,
		FTSTokenizer: defaultFTSTokenizer,
		Extraction: ExtractionConfig{
			Timeout:      time.Minute,
			MaxFileBytes: 50 * 1024 * 1024,
			PDF:          PDFExtractionConfig{Enabled: true, Backend: "poppler", Binary: "pdftotext"},
			DOCX:         DOCXExtractionConfig{Enabled: true, Backend: "native"},
			PPTX:         PPTXExtractionConfig{Enabled: true, Backend: "native", IncludeNotes: true},
		},
	}
}

func cloneHashTestConfig(cfg resolvedConfig) resolvedConfig {
	cfg.Include = append([]string(nil), cfg.Include...)
	cfg.Exclude = append([]string(nil), cfg.Exclude...)
	return cfg
}

func manifestFromResolved(cfg resolvedConfig) manifest {
	return manifest{
		SchemaVersion: schemaVersion,
		AgentKey:      cfg.AgentKey,
		WorkspaceRoot: cfg.WorkspaceRoot,
		Embedding:     cfg.Embedding,
		Include:       append([]string(nil), cfg.Include...),
		Exclude:       append([]string(nil), cfg.Exclude...),
		Chunk:         cfg.Chunk,
		Retrieval:     cfg.Retrieval,
		Extraction:    cfg.Extraction,
		Storage:       cfg.Storage,
		FTSTokenizer:  cfg.FTSTokenizer,
	}
}

func writeTestManifest(t *testing.T, storage string, value manifest) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storage, "manifest.json"), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
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

func (s *hashWorkspaceStore) ClearIndex() error {
	s.clearCalls++
	return nil
}

func (s *hashWorkspaceStore) File(string) (*fileRecord, error) { return nil, nil }

func (s *hashWorkspaceStore) ActiveFilePaths() (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (s *hashWorkspaceStore) UpsertSkippedFile(fileRecord) error { return nil }

func (s *hashWorkspaceStore) UpsertIndexedFile(fileRecord, []chunkRecord) error { return nil }

func (s *hashWorkspaceStore) MarkDeleted(string) error { return nil }

var _ workspaceIndexStore = (*hashWorkspaceStore)(nil)
