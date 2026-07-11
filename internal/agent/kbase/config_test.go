package kbase

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestParseAgentConfigDefaultsLegacyCharsAndAliases(t *testing.T) {
	defaults, err := ParseAgentConfig(nil)
	if err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	if defaults.Storage.Location != "runtime" ||
		defaults.Chunk.Unit != ChunkUnitEstimatedTokens ||
		defaults.Chunk.MaxTokens != 1000 ||
		defaults.Chunk.OverlapTokens != 100 ||
		defaults.Retrieval.TopK != 8 {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}
	if !reflect.DeepEqual(defaults.Include, DefaultIncludePatterns()) || !reflect.DeepEqual(defaults.Exclude, DefaultExcludePatterns()) {
		t.Fatalf("unexpected default scope: %#v", defaults)
	}

	legacy, err := ParseAgentConfig(map[string]any{
		"chunk": map[string]any{"max-chars": 3200, "overlap-chars": 320},
		"retrieval": map[string]any{
			"top-k":         12,
			"vector-weight": 0.6,
			"fts-weight":    0.4,
		},
	})
	if err != nil {
		t.Fatalf("parse legacy config: %v", err)
	}
	if legacy.Chunk.Unit != ChunkUnitChars || legacy.Chunk.MaxChars != 3200 || legacy.Chunk.OverlapChars != 320 ||
		legacy.Chunk.MaxTokens != 0 || legacy.Chunk.OverlapTokens != 0 {
		t.Fatalf("legacy char config changed: %#v", legacy.Chunk)
	}
	if legacy.Retrieval.TopK != 12 || legacy.Retrieval.VectorWeight != 0.6 || legacy.Retrieval.FTSWeight != 0.4 {
		t.Fatalf("retrieval aliases changed: %#v", legacy.Retrieval)
	}

	dualWritten, err := ParseAgentConfig(map[string]any{
		"retrieval": map[string]any{
			"topK":          7,
			"top-k":         13,
			"vectorWeight":  0.8,
			"vector-weight": 0.55,
			"ftsWeight":     0.2,
			"fts-weight":    0.45,
		},
	})
	if err != nil {
		t.Fatalf("parse dual-written retrieval config: %v", err)
	}
	if dualWritten.Retrieval.TopK != 13 || dualWritten.Retrieval.VectorWeight != 0.55 || dualWritten.Retrieval.FTSWeight != 0.45 {
		t.Fatalf("legacy kebab keys must retain their historical override precedence: %#v", dualWritten.Retrieval)
	}
}

func TestValidateAgentConfigSchemaRejectsRemovedEmbeddingFields(t *testing.T) {
	for _, key := range []string{"providerKey", "model", "dimension", "timeout"} {
		_, err := ParseAgentConfig(map[string]any{
			"embedding": map[string]any{key: "legacy"},
		})
		if err == nil {
			t.Fatalf("expected removed embedding field %q to fail", key)
		}
	}
	if _, err := ParseAgentConfig(map[string]any{"chunk": map[string]any{"unit": "bytes"}}); err == nil {
		t.Fatal("expected invalid chunk unit to fail")
	}
}

func TestComputeConfigHashGolden(t *testing.T) {
	cfg := resolvedConfig{
		AgentKey:      "docs",
		WorkspaceRoot: "/workspace/docs",
		StorageDir:    "/runtime/kbase/docs",
		Storage:       "runtime",
		Embedding: EmbeddingSnapshot{
			ModelKey:     "embedding-key",
			ProviderKey:  "provider",
			Model:        "text-embedding",
			Dimension:    1536,
			Timeout:      15,
			EndpointPath: "/v1/embeddings",
		},
		Include:   DefaultIncludePatterns(),
		Exclude:   DefaultExcludePatterns(),
		Chunk:     DefaultChunkConfig(),
		Retrieval: RetrievalConfig{TopK: 8, VectorWeight: 0.7, FTSWeight: 0.3},
		Extraction: ExtractionConfig{
			Timeout:      time.Minute,
			MaxFileBytes: 50 * 1024 * 1024,
			PDF:          PDFExtractionConfig{Enabled: true, Backend: "poppler", Binary: "pdftotext"},
			DOCX:         DOCXExtractionConfig{Enabled: true, Backend: "native"},
			PPTX:         PPTXExtractionConfig{Enabled: true, Backend: "native", IncludeNotes: true},
		},
	}
	const want = "sha256:1f78cec3245c0404f7e51c3edd4b751074112589d96a4e36e7199986c172cd08"
	if got := computeConfigHash(cfg); got != want {
		t.Fatalf("config hash changed: got %q want %q", got, want)
	}
}

func TestReconcileWatchersRebindsChangedAgentAndStopsDeletedAgent(t *testing.T) {
	root := t.TempDir()
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("mkdir workspace: %v", err)
		}
	}
	source := &stubAgentSource{agents: map[string]AgentSpec{
		"docs": testKBaseAgent("docs", workspaceA, "runtime"),
	}}
	manager := NewManager(ManagerOptions{RefreshDebounce: 10 * time.Millisecond}, source, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.ReconcileWatchers(ctx)
	manager.mu.Lock()
	first := manager.watchers["docs"]
	manager.mu.Unlock()
	if first.watcher == nil || first.cancel == nil || first.signature == "" {
		t.Fatalf("missing first watcher binding: %#v", first)
	}

	changed := testKBaseAgent("docs", workspaceB, "runtime")
	changed.Config.Include = append(changed.Config.Include, "**/*.rst")
	source.agents["docs"] = changed
	reloadCtx, cancelReload := context.WithCancel(context.Background())
	manager.ReconcileWatchers(reloadCtx)
	cancelReload()
	manager.mu.Lock()
	second := manager.watchers["docs"]
	manager.mu.Unlock()
	if second.watcher == nil || second.signature == first.signature {
		t.Fatalf("watcher was not rebound: first=%#v second=%#v", first, second)
	}
	waitDone(t, first.watcher.Done())
	select {
	case <-second.watcher.Done():
		t.Fatal("rebound watcher inherited the short-lived reload context")
	case <-time.After(50 * time.Millisecond):
	}

	delete(source.agents, "docs")
	manager.ReconcileWatchers(context.Background())
	manager.mu.Lock()
	_, exists := manager.watchers["docs"]
	manager.mu.Unlock()
	if exists {
		t.Fatal("deleted agent watcher remains registered")
	}
	waitDone(t, second.watcher.Done())
}

func TestReconcileWatchersSerializesConcurrentCatalogReloads(t *testing.T) {
	root := t.TempDir()
	workspaces := make([]string, 6)
	for index := range workspaces {
		workspaces[index] = filepath.Join(root, "workspace-"+strconv.Itoa(index))
		if err := os.MkdirAll(workspaces[index], 0o755); err != nil {
			t.Fatalf("mkdir workspace: %v", err)
		}
	}
	source := &synchronizedAgentSource{agents: map[string]AgentSpec{}}
	manager := NewManager(ManagerOptions{RefreshDebounce: 10 * time.Millisecond}, source, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for index := 0; index < 24; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			spec := testKBaseAgent("docs", workspaces[index%len(workspaces)], "runtime")
			spec.Config.Include = append(spec.Config.Include, "variant-"+strconv.Itoa(index))
			source.Set("docs", spec)
			manager.ReconcileWatchers(ctx)
		}()
	}
	wg.Wait()

	current, ok := source.Agent("docs")
	if !ok {
		t.Fatal("missing current source definition")
	}
	manager.mu.Lock()
	binding := manager.watchers["docs"]
	manager.mu.Unlock()
	if binding.watcher == nil || binding.signature != watcherSignature(current) {
		t.Fatalf("watcher does not match latest catalog snapshot: binding=%#v current=%#v", binding, current)
	}
	cancel()
	waitDone(t, binding.watcher.Done())
}

type synchronizedAgentSource struct {
	mu     sync.RWMutex
	agents map[string]AgentSpec
}

func (s *synchronizedAgentSource) Agents() []AgentSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentSpec, 0, len(s.agents))
	for _, spec := range s.agents {
		out = append(out, spec)
	}
	return out
}

func (s *synchronizedAgentSource) Agent(key string) (AgentSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	spec, ok := s.agents[key]
	return spec, ok
}

func (s *synchronizedAgentSource) Set(key string, spec AgentSpec) {
	s.mu.Lock()
	s.agents[key] = spec
	s.mu.Unlock()
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}
