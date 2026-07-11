package kbase

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/models"
)

type stubAgentSource struct {
	agents map[string]AgentSpec
}

func (r stubAgentSource) Agents() []AgentSpec {
	out := make([]AgentSpec, 0, len(r.agents))
	for _, def := range r.agents {
		out = append(out, def)
	}
	return out
}

func (r stubAgentSource) Agent(key string) (AgentSpec, bool) {
	def, ok := r.agents[key]
	return def, ok
}

func newKBaseTestModelRegistry(t *testing.T, root string, handler http.HandlerFunc) *models.ModelRegistry {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	registriesDir := filepath.Join(root, "registries")
	providersDir := filepath.Join(registriesDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	modelsDir := filepath.Join(registriesDir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: " + server.URL,
		"apiKey: test-key",
		"embedding:",
		"  model: mock-embedding",
		"  dimension: 3",
		"  timeout: 5",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-embedding-key.yml"), []byte(strings.Join([]string{
		"key: mock-embedding-key",
		"provider: mock",
		"type: embedding",
		"modelId: mock-embedding-from-model-key",
		"embedding:",
		"  dimension: 3",
		"  timeout: 7",
		"  endpointPath: /custom/embeddings",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write embedding model: %v", err)
	}
	modelRegistry, err := models.LoadModelRegistry(registriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return modelRegistry
}

func testKBaseAgent(key string, workspace string, storage string) AgentSpec {
	return AgentSpec{
		Key:           key,
		Mode:          Mode,
		WorkspaceRoot: workspace,
		Config: AgentConfig{
			Embedding: EmbeddingConfig{ModelKey: "mock-embedding-key"},
			Storage:   StorageConfig{Location: storage},
			Include:   []string{"**/*.md", "**/*.txt"},
			Exclude:   []string{".git/**", ".kbase/**", "node_modules/**"},
			Chunk:     ChunkConfig{Unit: ChunkUnitEstimatedTokens, MaxTokens: 1000, OverlapTokens: 100},
			Retrieval: RetrievalConfig{TopK: 5, VectorWeight: 0.7, FTSWeight: 0.3},
		},
	}
}

func testManagerOptions(runtimeDir string) ManagerOptions {
	return ManagerOptions{RuntimeDir: runtimeDir}
}

func testEmbeddingHandler(t *testing.T, requests *atomic.Int64) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" && r.URL.Path != "/custom/embeddings" {
			http.NotFound(w, r)
			return
		}
		if requests != nil {
			requests.Add(1)
		}
		inputs := decodeEmbeddingInputs(t, r)
		writeEmbeddingResponse(w, inputs)
	}
}

func decodeEmbeddingInputs(t *testing.T, r *http.Request) []string {
	t.Helper()
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode embedding request: %v", err)
	}
	return req.Input
}

func writeEmbeddingResponse(w http.ResponseWriter, inputs []string) {
	resp := map[string]any{"data": []map[string]any{}}
	data := resp["data"].([]map[string]any)
	for i, text := range inputs {
		lower := strings.ToLower(text)
		vector := []float64{0, 0, 1}
		if strings.Contains(lower, "alpha") {
			vector[0] = 1
		}
		if strings.Contains(lower, "beta") {
			vector[1] = 1
		}
		data = append(data, map[string]any{"index": i, "embedding": vector})
	}
	resp["data"] = data
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func recordMax(max *atomic.Int64, value int64) {
	for {
		current := max.Load()
		if value <= current || max.CompareAndSwap(current, value) {
			return
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if condition() {
		return
	}
	t.Fatalf("condition not met within %s", timeout)
}

func kbaseIndexRunCount(t *testing.T, storageDir string) int {
	t.Helper()
	store, err := OpenReadStore(storageDir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("open read store for run count: %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM KBASE_INDEX_RUNS`).Scan(&count); err != nil {
		t.Fatalf("count index runs: %v", err)
	}
	return count
}

func TestOpenReadStoreAndManagerReadDoNotCreateMissingDB(t *testing.T) {
	root := t.TempDir()
	missingStore := filepath.Join(root, "missing-store")
	store, err := OpenReadStore(missingStore)
	if !os.IsNotExist(err) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("OpenReadStore missing error = %v, want os.IsNotExist", err)
	}
	if _, statErr := os.Stat(missingStore); !os.IsNotExist(statErr) {
		t.Fatalf("OpenReadStore created store dir, stat err = %v", statErr)
	}

	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	modelRegistry := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, nil))
	def := testKBaseAgent("docs", workspace, "runtime")
	runtimeDir := filepath.Join(root, "kbase")
	manager := NewManager(testManagerOptions(runtimeDir), stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)
	storageDir := filepath.Join(runtimeDir, "docs")

	status, err := manager.Status("docs")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Stale || status.Files != 0 || status.Chunks != 0 {
		t.Fatalf("unexpected status for missing DB: %#v", status)
	}
	if status.Chunk.Unit != ChunkUnitEstimatedTokens || status.Chunk.MaxTokens != 1000 || status.Chunk.OverlapTokens != 100 {
		t.Fatalf("unexpected status chunk config: %#v", status.Chunk)
	}
	if _, statErr := os.Stat(storageDir); !os.IsNotExist(statErr) {
		t.Fatalf("Status created storage dir, stat err = %v", statErr)
	}

	read, err := manager.Read("docs", ReadOptions{Path: "alpha.md"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Found || read.Path != "alpha.md" {
		t.Fatalf("unexpected read for missing DB: %#v", read)
	}
	if _, statErr := os.Stat(storageDir); !os.IsNotExist(statErr) {
		t.Fatalf("Read created storage dir, stat err = %v", statErr)
	}
}

func TestManagerResolveUsesKBaseEmbeddingDefaults(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	modelRegistry := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, nil))
	def := testKBaseAgent("docs", workspace, "runtime")
	def.Config.Embedding = EmbeddingConfig{}
	options := testManagerOptions(filepath.Join(root, "kbase"))
	options.DefaultEmbeddingModelKey = "mock-embedding-key"
	manager := NewManager(options, stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)

	resolved, embedder, err := manager.resolve("docs")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Embedding.ModelKey != "mock-embedding-key" ||
		resolved.Embedding.ProviderKey != "mock" ||
		resolved.Embedding.Model != "mock-embedding-from-model-key" ||
		resolved.Embedding.Dimension != 3 ||
		resolved.Embedding.Timeout != 7 {
		t.Fatalf("unexpected resolved embedding defaults: %#v", resolved.Embedding)
	}
	if embedder == nil || embedder.Model != "mock-embedding-from-model-key" || embedder.Dimension != 3 || embedder.Timeout != 7 {
		t.Fatalf("unexpected embedder defaults: %#v", embedder)
	}
}

func TestManagerResolveUsesKBaseEmbeddingModelKey(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	modelRegistry := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, nil))
	def := testKBaseAgent("docs", workspace, "runtime")
	def.Config.Embedding = EmbeddingConfig{
		ModelKey: "mock-embedding-key",
	}
	options := testManagerOptions(filepath.Join(root, "kbase"))
	options.DefaultEmbeddingModelKey = "settings-embedding-key"
	manager := NewManager(options, stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)

	resolved, embedder, err := manager.resolve("docs")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Embedding.ModelKey != "mock-embedding-key" ||
		resolved.Embedding.ProviderKey != "mock" ||
		resolved.Embedding.Model != "mock-embedding-from-model-key" ||
		resolved.Embedding.Dimension != 3 ||
		resolved.Embedding.Timeout != 7 ||
		resolved.Embedding.EndpointPath != "/custom/embeddings" {
		t.Fatalf("unexpected resolved embedding modelKey config: %#v", resolved.Embedding)
	}
	if embedder == nil ||
		embedder.Model != "mock-embedding-from-model-key" ||
		embedder.Dimension != 3 ||
		embedder.Timeout != 7 ||
		embedder.EndpointPath != "/custom/embeddings" {
		t.Fatalf("unexpected embedder modelKey config: %#v", embedder)
	}
}

func TestManagerResolveRejectsNonEmbeddingModelKey(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	modelRegistry := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, nil))
	modelsDir := filepath.Join(root, "registries", "models")
	if err := os.WriteFile(filepath.Join(modelsDir, "chat-model.yml"), []byte(strings.Join([]string{
		"key: chat-model",
		"provider: mock",
		"type: chat",
		"modelId: chat-model-id",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write chat model: %v", err)
	}
	if err := modelRegistry.ReloadModels(); err != nil {
		t.Fatalf("reload models: %v", err)
	}
	def := testKBaseAgent("docs", workspace, "runtime")
	def.Config.Embedding = EmbeddingConfig{ModelKey: "chat-model"}
	manager := NewManager(testManagerOptions(filepath.Join(root, "kbase")), stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)

	if _, _, err := manager.resolve("docs"); err == nil || !strings.Contains(err.Error(), "want embedding") {
		t.Fatalf("expected non-embedding modelKey error, got %v", err)
	}
}

func TestStoreOpenModesConfigureSQLitePragmas(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	var journalMode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	readStore, err := OpenReadStore(root)
	if err != nil {
		t.Fatalf("open read store: %v", err)
	}
	defer readStore.Close()
	if err := readStore.SetMeta("queryOnly", "blocked"); err == nil {
		t.Fatal("read store SetMeta succeeded, want query_only write failure")
	}
}

func TestStoreBusyTimeoutWaitsForWriter(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.SetMeta("seed", "ready"); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	holder, err := sql.Open("sqlite", kbaseSQLiteDSN(filepath.Join(root, "kbase.db"), sqliteOpenWrite))
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	holder.SetMaxOpenConns(1)
	holder.SetMaxIdleConns(1)
	defer holder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}

	blocked := make(chan error, 1)
	go func() {
		waitingStore, err := OpenStore(root)
		if err != nil {
			blocked <- err
			return
		}
		defer waitingStore.Close()
		blocked <- waitingStore.SetMeta("blocked", "released")
	}()

	select {
	case err := <-blocked:
		t.Fatalf("write finished before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("commit holder: %v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("waiting write failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting write did not finish after lock release")
	}

	verify, err := OpenReadStore(root)
	if err != nil {
		t.Fatalf("open verify: %v", err)
	}
	defer verify.Close()
	if got := verify.Meta("blocked"); got != "released" {
		t.Fatalf("blocked meta = %q, want released", got)
	}
}

func TestManagerConcurrentSearchDuringRefreshDoesNotReturnBusy(t *testing.T) {
	var embeddingRequests atomic.Int64
	root := t.TempDir()
	modelRegistry := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, &embeddingRequests))
	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Alpha\nalpha overview"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	def := testKBaseAgent("docs", workspace, "runtime")
	manager := NewManager(testManagerOptions(filepath.Join(root, "kbase")), stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)
	if _, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "initial"}); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	for i := 0; i < 20; i++ {
		path := filepath.Join(workspace, "new-"+strconv.Itoa(i)+".md")
		if err := os.WriteFile(path, []byte("alpha beta concurrent refresh material"), 0o644); err != nil {
			t.Fatalf("write new doc: %v", err)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "manual"})
		errs <- err
	}()
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := manager.Search(ctx, "docs", "alpha", SearchOptions{Limit: 5})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent search/refresh error: %v", err)
		}
	}
}

func TestManagerRefreshSerializesSharedStorageDir(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Alpha\nalpha shared storage"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}

	var active atomic.Int64
	var maxActive atomic.Int64
	var calls atomic.Int64
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" && r.URL.Path != "/custom/embeddings" {
			http.NotFound(w, r)
			return
		}
		call := calls.Add(1)
		if call == 1 {
			close(firstStarted)
		}
		current := active.Add(1)
		recordMax(&maxActive, current)
		defer active.Add(-1)
		<-release
		inputs := decodeEmbeddingInputs(t, r)
		writeEmbeddingResponse(w, inputs)
	}
	modelRegistry := newKBaseTestModelRegistry(t, root, handler)
	defA := testKBaseAgent("docs_a", workspace, "workspace")
	defB := testKBaseAgent("docs_b", workspace, "workspace")
	manager := NewManager(testManagerOptions(filepath.Join(root, "kbase")), stubAgentSource{agents: map[string]AgentSpec{
		"docs_a": defA,
		"docs_b": defB,
	}}, modelRegistry)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"docs_a", "docs_b"} {
		agentKey := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := manager.Refresh(context.Background(), agentKey, RefreshOptions{Mode: "manual"})
			errs <- err
		}()
	}
	close(start)
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first embedding request did not start")
	}
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("shared storage refresh failed: %v", err)
		}
	}
	if got := maxActive.Load(); got > 1 {
		t.Fatalf("shared storage refreshes overlapped embedding work, max active = %d", got)
	}
}

func TestManagerStaleSearchQueuesSingleRefresh(t *testing.T) {
	var embeddingRequests atomic.Int64
	root := t.TempDir()
	modelRegistry := newKBaseTestModelRegistry(t, root, testEmbeddingHandler(t, &embeddingRequests))
	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Alpha\nalpha queued refresh"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	def := testKBaseAgent("docs", workspace, "runtime")
	runtimeDir := filepath.Join(root, "kbase")
	manager := NewManager(testManagerOptions(runtimeDir), stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)
	storageDir := filepath.Join(runtimeDir, "docs")

	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := manager.Search(context.Background(), "docs", "alpha", SearchOptions{Limit: 3})
			// The queued refresh may finish before a later concurrent search opens
			// the store. A missing result must be marked stale; an indexed result is
			// also valid as long as all callers share the single queued refresh.
			if err == nil && result.Count == 0 && !result.Stale {
				err = fmt.Errorf("unexpected missing-index search result: %#v", result)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("stale search error: %v", err)
		}
	}

	waitFor(t, 3*time.Second, func() bool {
		status, err := manager.Status("docs")
		if err != nil || status.Stale || status.Files != 1 {
			return false
		}
		return kbaseIndexRunCount(t, storageDir) == 1 && embeddingRequests.Load() == 1
	})
	time.Sleep(150 * time.Millisecond)
	if got := kbaseIndexRunCount(t, storageDir); got != 1 {
		t.Fatalf("index run count = %d, want one queued refresh", got)
	}
	if got := embeddingRequests.Load(); got != 1 {
		t.Fatalf("embedding request count = %d, want 1", got)
	}
}

func TestManagerRefreshSearchReadAndIgnoreKBaseDir(t *testing.T) {
	embeddingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode embedding request: %v", err)
		}
		resp := map[string]any{"data": []map[string]any{}}
		data := resp["data"].([]map[string]any)
		for i, text := range req.Input {
			lower := strings.ToLower(text)
			vector := []float64{0, 0, 1}
			if strings.Contains(lower, "alpha") {
				vector[0] = 1
			}
			if strings.Contains(lower, "beta") {
				vector[1] = 1
			}
			data = append(data, map[string]any{"index": i, "embedding": vector})
		}
		resp["data"] = data
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer embeddingServer.Close()

	root := t.TempDir()
	registriesDir := filepath.Join(root, "registries")
	providersDir := filepath.Join(registriesDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	modelsDir := filepath.Join(registriesDir, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: " + embeddingServer.URL,
		"apiKey: test-key",
		"embedding:",
		"  model: mock-embedding",
		"  dimension: 3",
		"  timeout: 5",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-embedding-key.yml"), []byte(strings.Join([]string{
		"key: mock-embedding-key",
		"provider: mock",
		"type: embedding",
		"modelId: mock-embedding",
		"embedding:",
		"  dimension: 3",
		"  timeout: 5",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write embedding model: %v", err)
	}
	modelRegistry, err := models.LoadModelRegistry(registriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}

	workspace := filepath.Join(root, "docs")
	if err := os.MkdirAll(filepath.Join(workspace, ".kbase"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "alpha.md"), []byte("# Alpha\nalpha overview"), 0o644); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "beta.txt"), []byte("beta reference material"), 0o644); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "guide.html"), []byte(`<!doctype html>
<html>
<head><style>.noise { color: red; }</style><script>delta script noise</script></head>
<body>
  <h1>Delta Guide</h1>
  <p>delta html reference material</p>
  <p hidden>hidden delta material</p>
</body>
</html>`), 0o644); err != nil {
		t.Fatalf("write guide html: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "guides", "deep"), 0o755); err != nil {
		t.Fatalf("mkdir guides: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "guides", "auth-policy.md"), []byte("# Auth Policy\nscoped auth policy"), 0o644); err != nil {
		t.Fatalf("write auth policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "guides", "deep", "auth-appendix.md"), []byte("# Auth Appendix\nscoped auth appendix"), 0o644); err != nil {
		t.Fatalf("write auth appendix: %v", err)
	}
	deck := zipFixture(t, map[string]string{
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>gamma slide insight</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>
</p:sld>`,
	})
	if err := os.WriteFile(filepath.Join(workspace, "deck.pptx"), deck, 0o644); err != nil {
		t.Fatalf("write deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".kbase", "hidden.md"), []byte("hidden beta"), 0o644); err != nil {
		t.Fatalf("write hidden: %v", err)
	}
	def := AgentSpec{
		Key:           "docs",
		Mode:          Mode,
		WorkspaceRoot: workspace,
		Config: AgentConfig{
			Embedding: EmbeddingConfig{ModelKey: "mock-embedding-key"},
			Storage:   StorageConfig{Location: "runtime"},
			Include:   []string{"**/*.md", "**/*.txt", "**/*.html", "**/*.htm", "**/*.pptx"},
			Exclude:   []string{".git/**", ".kbase/**", "node_modules/**"},
			Chunk:     ChunkConfig{Unit: ChunkUnitEstimatedTokens, MaxTokens: 1000, OverlapTokens: 100},
			Retrieval: RetrievalConfig{TopK: 5, VectorWeight: 0.7, FTSWeight: 0.3},
		},
	}
	runtimeDir := filepath.Join(root, "kbase")
	manager := NewManager(testManagerOptions(runtimeDir), stubAgentSource{agents: map[string]AgentSpec{"docs": def}}, modelRegistry)

	refresh, err := manager.Refresh(context.Background(), "docs", RefreshOptions{Mode: "manual"})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refresh.Status != "success" || refresh.ScannedFiles != 6 {
		t.Fatalf("unexpected refresh result: %#v", refresh)
	}
	status, err := manager.Status("docs")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Files != 6 || status.Chunks == 0 || status.Stale {
		t.Fatalf("unexpected status: %#v", status)
	}
	search, err := manager.Search(context.Background(), "docs", "beta", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if search.Count == 0 || search.Results[0].Path != "beta.txt" {
		t.Fatalf("expected beta.txt top hit, got %#v", search)
	}
	read, err := manager.Read("docs", ReadOptions{ChunkID: search.Results[0].ChunkID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !read.Found || !strings.Contains(read.Content, "beta reference") {
		t.Fatalf("unexpected read result: %#v", read)
	}
	htmlSearch, err := manager.Search(context.Background(), "docs", "delta", SearchOptions{Limit: 3, Type: "html"})
	if err != nil {
		t.Fatalf("html search: %v", err)
	}
	if htmlSearch.Count == 0 || htmlSearch.Results[0].Path != "guide.html" || htmlSearch.Results[0].SourceType != "html" {
		t.Fatalf("expected guide.html html hit, got %#v", htmlSearch)
	}
	htmlRead, err := manager.Read("docs", ReadOptions{ChunkID: htmlSearch.Results[0].ChunkID})
	if err != nil {
		t.Fatalf("html read: %v", err)
	}
	if !htmlRead.Found || htmlRead.SourceType != "html" || !strings.Contains(htmlRead.Content, "delta html reference material") || strings.Contains(htmlRead.Content, "script noise") || strings.Contains(htmlRead.Content, "hidden delta") {
		t.Fatalf("unexpected html read result: %#v", htmlRead)
	}
	slideSearch, err := manager.Search(context.Background(), "docs", "gamma", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatalf("slide search: %v", err)
	}
	if slideSearch.Count == 0 || slideSearch.Results[0].Path != "deck.pptx" || slideSearch.Results[0].SlideStart != 1 || slideSearch.Results[0].SourceType != "pptx" {
		t.Fatalf("expected deck.pptx slide hit, got %#v", slideSearch)
	}
	slideRead, err := manager.Read("docs", ReadOptions{ChunkID: slideSearch.Results[0].ChunkID})
	if err != nil {
		t.Fatalf("slide read: %v", err)
	}
	if !slideRead.Found || slideRead.SlideStart != 1 || slideRead.SourceType != "pptx" || !strings.Contains(slideRead.Content, "gamma slide") {
		t.Fatalf("unexpected slide read result: %#v", slideRead)
	}

	scoped, err := manager.Search(context.Background(), "docs", "auth", SearchOptions{PathPrefix: "guides/", Limit: 10})
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	if scoped.Count != 2 || scoped.MatchCount != 2 || scoped.Truncated {
		t.Fatalf("unexpected scoped search counts: %#v", scoped)
	}
	for _, hit := range scoped.Results {
		if !strings.HasPrefix(hit.Path, "guides/") {
			t.Fatalf("scoped search returned out-of-scope hit: %#v", scoped)
		}
	}
	globbed, err := manager.Search(context.Background(), "docs", "auth", SearchOptions{PathGlob: "**/*policy.md", Limit: 10})
	if err != nil {
		t.Fatalf("globbed search: %v", err)
	}
	if globbed.Count != 1 || globbed.Results[0].Path != "guides/auth-policy.md" {
		t.Fatalf("unexpected globbed search: %#v", globbed)
	}
	firstPage, err := manager.Search(context.Background(), "docs", "auth", SearchOptions{PathPrefix: "guides", Type: ".md", Limit: 1})
	if err != nil {
		t.Fatalf("typed search first page: %v", err)
	}
	if firstPage.Count != 1 || firstPage.MatchCount != 2 || !firstPage.Truncated {
		t.Fatalf("unexpected typed first page: %#v", firstPage)
	}
	secondPage, err := manager.Search(context.Background(), "docs", "auth", SearchOptions{PathPrefix: "guides", Type: "md", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("typed search second page: %v", err)
	}
	if secondPage.Count != 1 || secondPage.MatchCount != 2 || secondPage.Truncated || secondPage.Results[0].Path == firstPage.Results[0].Path {
		t.Fatalf("unexpected typed second page: %#v first=%#v", secondPage, firstPage)
	}

	files, err := manager.Files("docs", FilesOptions{Path: "guides", Pattern: "*.md", HeadLimit: -1})
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if files.Mode != "files" || files.MatchCount != 1 || files.Results[0].Path != "guides/auth-policy.md" {
		t.Fatalf("unexpected files result: %#v", files)
	}
	tree, err := manager.Files("docs", FilesOptions{Mode: "tree", Path: "guides", Pattern: "**/*.md", Depth: 1, HeadLimit: -1})
	if err != nil {
		t.Fatalf("tree files: %v", err)
	}
	if tree.FileCount != 2 || tree.DirCount != 1 || tree.MatchCount != 2 {
		t.Fatalf("unexpected tree counts: %#v", tree)
	}
	if !hasKBaseFileEntry(tree.Results, "dir", "guides/deep/") || !hasKBaseFileEntry(tree.Results, "file", "guides/auth-policy.md") {
		t.Fatalf("unexpected tree entries: %#v", tree.Results)
	}
	store, err := OpenStore(filepath.Join(runtimeDir, "docs"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.UpsertSkippedFile(fileRecord{
		ID:         fileID("skip.bin"),
		Path:       "skip.bin",
		Ext:        ".bin",
		Size:       42,
		Status:     "skipped",
		SkipReason: "unsupported_extension",
		IndexedAt:  time.Now().UnixMilli(),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("insert skipped file: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	activeFiles, err := manager.Files("docs", FilesOptions{HeadLimit: -1})
	if err != nil {
		t.Fatalf("active files: %v", err)
	}
	if activeFiles.FileCount != 6 || hasKBaseFileEntry(activeFiles.Results, "file", "skip.bin") {
		t.Fatalf("default files should only include active files: %#v", activeFiles)
	}
	allFiles, err := manager.Files("docs", FilesOptions{Status: "all", Type: "bin", HeadLimit: -1})
	if err != nil {
		t.Fatalf("all files: %v", err)
	}
	if allFiles.FileCount != 1 || !hasKBaseFileEntry(allFiles.Results, "file", "skip.bin") {
		t.Fatalf("expected skipped bin in status=all result: %#v", allFiles)
	}
}

func hasKBaseFileEntry(entries []FileEntry, typ string, path string) bool {
	for _, entry := range entries {
		if entry.Type == typ && entry.Path == path {
			return true
		}
	}
	return false
}
