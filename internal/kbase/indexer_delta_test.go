package kbase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexWorkspacePathsDeletesOnlyRequestedFile(t *testing.T) {
	store := newDeltaTestStore()
	store.files["docs/a.md"] = fileRecord{ID: fileID("docs/a.md"), Path: "docs/a.md", Status: "active"}
	store.files["docs/b.md"] = fileRecord{ID: fileID("docs/b.md"), Path: "docs/b.md", Status: "active"}
	cfg := deltaTestConfig(t.TempDir())
	run := IndexRun{}
	if err := indexWorkspacePaths(context.Background(), store, cfg, nil, []string{"docs/a.md"}, &run); err != nil {
		t.Fatalf("index delta: %v", err)
	}
	if run.CandidatePaths != 1 || run.ScannedFiles != 0 || run.DeletedFiles != 1 {
		t.Fatalf("unexpected run: %#v", run)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "docs/a.md" {
		t.Fatalf("deleted paths = %#v", store.deleted)
	}
}

func TestIndexWorkspacePathsDeletesRemovedDirectoryPrefix(t *testing.T) {
	store := newDeltaTestStore()
	for _, path := range []string{"docs/a.md", "docs/sub/b.md", "other/c.md"} {
		store.files[path] = fileRecord{ID: fileID(path), Path: path, Status: "active"}
	}
	cfg := deltaTestConfig(t.TempDir())
	run := IndexRun{}
	if err := indexWorkspacePaths(context.Background(), store, cfg, nil, []string{"docs"}, &run); err != nil {
		t.Fatalf("index delta: %v", err)
	}
	if run.DeletedFiles != 2 || len(store.deleted) != 2 {
		t.Fatalf("deleted=%d paths=%#v", run.DeletedFiles, store.deleted)
	}
}

func TestIndexWorkspacePathsTreatsRenameAsDeleteAndAdd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "new.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("renamed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newDeltaTestStore()
	store.files["docs/old.log"] = fileRecord{ID: fileID("docs/old.log"), Path: "docs/old.log", Status: "active"}
	cfg := deltaTestConfig(root)
	cfg.Include = nil
	run := IndexRun{}
	if err := indexWorkspacePaths(context.Background(), store, cfg, nil, []string{"docs/old.log", "docs/new.log"}, &run); err != nil {
		t.Fatalf("index rename delta: %v", err)
	}
	if run.DeletedFiles != 1 || run.NewFiles != 1 || store.files["docs/new.log"].Status != "skipped" {
		t.Fatalf("unexpected rename run=%#v files=%#v", run, store.files)
	}
}

func TestIndexWorkspaceReconcilesActiveSkippedAndErrorRecords(t *testing.T) {
	store := newDeltaTestStore()
	for path, status := range map[string]string{
		"active.md": "active", "skipped.bin": "skipped", "failed.pdf": "error",
	} {
		store.files[path] = fileRecord{ID: fileID(path), Path: path, Status: status}
	}
	cfg := deltaTestConfig(t.TempDir())
	run := IndexRun{}
	if err := indexWorkspace(context.Background(), store, cfg, nil, false, &run); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if run.DeletedFiles != 3 || len(store.deleted) != 3 {
		t.Fatalf("reconcile left stale records: run=%#v deleted=%#v", run, store.deleted)
	}
}

func TestIndexWorkspacePathsMetadataOnlyWhenContentHashUnchanged(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "a.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("same content\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	store := newDeltaTestStore()
	store.files["docs/a.txt"] = fileRecord{
		ID: fileID("docs/a.txt"), Path: "docs/a.txt", Status: "active", Size: int64(len(content)),
		MTimeMS: 1, SHA256: shaHex(content), ChunkCount: 1, ChunkSetHash: "old", IndexedAt: 10,
	}
	cfg := deltaTestConfig(root)
	run := IndexRun{}
	if err := indexWorkspacePaths(context.Background(), store, cfg, nil, []string{"docs/a.txt"}, &run); err != nil {
		t.Fatalf("index delta: %v", err)
	}
	if run.MetadataOnlyFiles != 1 || run.ChangedFiles != 0 || store.metadataUpserts != 1 || len(store.indexedChunks) != 0 {
		t.Fatalf("unexpected run=%#v metadata=%d chunks=%d", run, store.metadataUpserts, len(store.indexedChunks))
	}
}

func TestIndexWorkspacePathsReusesUnchangedChunkEmbeddings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "docs", "a.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := deltaTestConfig(root)
	cfg.Chunk = ChunkConfig{Unit: ChunkUnitChars, MaxChars: 6}
	target := chunkText("docs/a.txt", content, cfg.Chunk, cfg.Embedding.Model, cfg.Embedding.Dimension)
	if len(target) < 2 {
		t.Fatalf("test requires multiple chunks, got %d", len(target))
	}
	store := newDeltaTestStore()
	store.files["docs/a.txt"] = fileRecord{ID: fileID("docs/a.txt"), Path: "docs/a.txt", Status: "active", MTimeMS: 1, SHA256: "old", ChunkSetHash: "old"}
	store.embeddings[target[0].ContentHash] = []float64{0.25, 0.75}

	embeddedInputs := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode embedding request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		embeddedInputs += len(request.Input)
		response := embeddingResponse{}
		for index := range request.Input {
			response.Data = append(response.Data, struct {
				Embedding []float64 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float64{1, float64(index)}, Index: index})
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	embedder := NewEmbedder(server.URL, "", cfg.Embedding.Model, cfg.Embedding.Dimension, 5)

	run := IndexRun{}
	if err := indexWorkspacePaths(context.Background(), store, cfg, embedder, []string{"docs/a.txt"}, &run); err != nil {
		t.Fatalf("index delta: %v", err)
	}
	if run.ReusedChunks != 1 || run.EmbeddedChunks != len(target)-1 || embeddedInputs != len(target)-1 {
		t.Fatalf("run=%#v embeddedInputs=%d target=%d", run, embeddedInputs, len(target))
	}
	if len(store.indexedChunks) != len(target) || store.indexedChunks[0].Embedding[0] != 0.25 {
		t.Fatalf("indexed chunks did not preserve cached vector: %#v", store.indexedChunks)
	}
}

func TestDeltaAccumulatorFallsBackToReconcile(t *testing.T) {
	accumulator := newDeltaAccumulator()
	for index := 0; index <= maxDeltaPaths; index++ {
		accumulator.Add(filepath.Join("docs", time.Unix(int64(index), 0).Format("150405.000000000")))
	}
	paths, reconcile := accumulator.Drain()
	if !reconcile || len(paths) != 0 {
		t.Fatalf("paths=%d reconcile=%t", len(paths), reconcile)
	}
}

func TestDeltaAccumulatorDeduplicatesAndCompactsParentPaths(t *testing.T) {
	accumulator := newDeltaAccumulator()
	for _, path := range []string{"docs/a.md", "docs/a.md", "docs/sub/b.md", "docs"} {
		accumulator.Add(path)
	}
	paths, reconcile := accumulator.Drain()
	if reconcile || len(paths) != 1 || paths[0] != "docs" {
		t.Fatalf("paths=%#v reconcile=%t", paths, reconcile)
	}
}

func TestDeltaQueueKeepsEventsArrivingDuringCurrentBatch(t *testing.T) {
	queue := &deltaQueue{paths: map[string]struct{}{}}
	queue.merge([]string{"docs/a.md"}, false)
	current, reconcile := queue.take()
	queue.merge([]string{"docs/b.md"}, false)
	if reconcile || len(current) != 1 || current[0] != "docs/a.md" {
		t.Fatalf("current=%#v reconcile=%t", current, reconcile)
	}
	next, reconcile := queue.take()
	if reconcile || len(next) != 1 || next[0] != "docs/b.md" {
		t.Fatalf("next=%#v reconcile=%t", next, reconcile)
	}
}

func TestDeltaQueueRequeuesFailedBatchWithoutDroppingNewEvents(t *testing.T) {
	queue := &deltaQueue{paths: map[string]struct{}{}}
	queue.merge([]string{"docs/a.md"}, false)
	current, reconcile := queue.take()
	queue.merge([]string{"docs/b.md"}, false)
	queue.merge(current, reconcile)
	retry, reconcile := queue.take()
	if reconcile || len(retry) != 2 || retry[0] != "docs/a.md" || retry[1] != "docs/b.md" {
		t.Fatalf("retry=%#v reconcile=%t", retry, reconcile)
	}
}

func deltaTestConfig(root string) resolvedConfig {
	return resolvedConfig{
		AgentKey: "docs", WorkspaceRoot: root, StorageDir: filepath.Join(root, ".storage"), Storage: "runtime",
		Embedding: EmbeddingSnapshot{Model: "embedding-test", Dimension: 2}, Include: []string{"**/*.md", "**/*.txt"},
		Chunk: DefaultChunkConfig(), Retrieval: DefaultAgentConfig().Retrieval,
		Extraction: ExtractionConfig{Timeout: time.Minute, MaxFileBytes: defaultMaxFileBytes}, FTSTokenizer: defaultFTSTokenizer,
	}
}

type deltaTestStore struct {
	meta            map[string]string
	files           map[string]fileRecord
	embeddings      map[string][]float64
	deleted         []string
	metadataUpserts int
	indexedChunks   []chunkRecord
}

func newDeltaTestStore() *deltaTestStore {
	return &deltaTestStore{meta: map[string]string{}, files: map[string]fileRecord{}, embeddings: map[string][]float64{}}
}

func (s *deltaTestStore) Meta(key string) string          { return s.meta[key] }
func (s *deltaTestStore) SetMeta(key, value string) error { s.meta[key] = value; return nil }
func (s *deltaTestStore) ClearIndex() error               { return nil }
func (s *deltaTestStore) File(path string) (*fileRecord, error) {
	record, ok := s.files[path]
	if !ok {
		return nil, nil
	}
	return &record, nil
}
func (s *deltaTestStore) TrackedFilePaths() (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for path, record := range s.files {
		if record.Status != "deleted" {
			out[path] = struct{}{}
		}
	}
	return out, nil
}
func (s *deltaTestStore) FileEmbeddings(string, string, int) (map[string][]float64, error) {
	return s.embeddings, nil
}
func (s *deltaTestStore) UpsertMetadataFile(record fileRecord) error {
	s.metadataUpserts++
	s.files[record.Path] = record
	return nil
}
func (s *deltaTestStore) UpsertSkippedFile(record fileRecord) error {
	s.files[record.Path] = record
	return nil
}
func (s *deltaTestStore) UpsertIndexedFile(record fileRecord, chunks []chunkRecord) error {
	s.files[record.Path] = record
	s.indexedChunks = append([]chunkRecord(nil), chunks...)
	return nil
}
func (s *deltaTestStore) MarkDeleted(path string) error {
	record := s.files[path]
	record.Status = "deleted"
	s.files[path] = record
	s.deleted = append(s.deleted, path)
	return nil
}

var _ workspaceIndexStore = (*deltaTestStore)(nil)
