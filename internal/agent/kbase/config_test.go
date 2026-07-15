package kbase

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
		defaults.Retrieval.TopK != 8 ||
		defaults.Retrieval.Fusion != RetrievalFusionRRF ||
		defaults.Retrieval.RRFK != 60 ||
		defaults.Retrieval.VectorWeight != 0.7 ||
		defaults.Retrieval.FTSWeight != 0.3 ||
		defaults.Retrieval.CandidateFloor != 30 ||
		defaults.Retrieval.CandidateMultiplier != 4 ||
		defaults.Retrieval.CandidateMax != 500 {
		t.Fatalf("unexpected defaults: %#v", defaults)
	}
	if !reflect.DeepEqual(defaults.Include, DefaultIncludePatterns()) || !reflect.DeepEqual(defaults.Exclude, DefaultExcludePatterns()) {
		t.Fatalf("unexpected default scope: %#v", defaults)
	}

	legacy, err := ParseAgentConfig(map[string]any{
		"chunk": map[string]any{"max-chars": 3200, "overlap-chars": 320},
		"retrieval": map[string]any{
			"top-k":                12,
			"fusion":               "RRF",
			"rrf-k":                42,
			"vector-weight":        0.6,
			"fts-weight":           0.4,
			"candidate-floor":      24,
			"candidate-multiplier": 5,
			"candidate-max":        240,
		},
	})
	if err != nil {
		t.Fatalf("parse legacy config: %v", err)
	}
	if legacy.Chunk.Unit != ChunkUnitChars || legacy.Chunk.MaxChars != 3200 || legacy.Chunk.OverlapChars != 320 ||
		legacy.Chunk.MaxTokens != 0 || legacy.Chunk.OverlapTokens != 0 {
		t.Fatalf("legacy char config changed: %#v", legacy.Chunk)
	}
	if legacy.Retrieval.TopK != 12 || legacy.Retrieval.Fusion != RetrievalFusionRRF || legacy.Retrieval.RRFK != 42 ||
		legacy.Retrieval.VectorWeight != 0.6 || legacy.Retrieval.FTSWeight != 0.4 ||
		legacy.Retrieval.CandidateFloor != 24 || legacy.Retrieval.CandidateMultiplier != 5 || legacy.Retrieval.CandidateMax != 240 {
		t.Fatalf("retrieval aliases changed: %#v", legacy.Retrieval)
	}

	dualWritten, err := ParseAgentConfig(map[string]any{
		"retrieval": map[string]any{
			"topK":                 7,
			"top-k":                13,
			"rrfK":                 61,
			"rrf-k":                62,
			"vectorWeight":         0.8,
			"vector-weight":        0.55,
			"ftsWeight":            0.2,
			"fts-weight":           0.45,
			"candidateFloor":       30,
			"candidate-floor":      31,
			"candidateMultiplier":  4,
			"candidate-multiplier": 6,
			"candidateMax":         500,
			"candidate-max":        600,
		},
	})
	if err != nil {
		t.Fatalf("parse dual-written retrieval config: %v", err)
	}
	if dualWritten.Retrieval.TopK != 13 || dualWritten.Retrieval.RRFK != 62 ||
		dualWritten.Retrieval.VectorWeight != 0.55 || dualWritten.Retrieval.FTSWeight != 0.45 ||
		dualWritten.Retrieval.CandidateFloor != 31 || dualWritten.Retrieval.CandidateMultiplier != 6 || dualWritten.Retrieval.CandidateMax != 600 {
		t.Fatalf("legacy kebab keys must retain their historical override precedence: %#v", dualWritten.Retrieval)
	}
}

func TestValidateAgentRetrievalConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RetrievalConfig)
	}{
		{name: "topK zero", mutate: func(cfg *RetrievalConfig) { cfg.TopK = 0 }},
		{name: "topK too high", mutate: func(cfg *RetrievalConfig) { cfg.TopK = 51 }},
		{name: "unsupported fusion", mutate: func(cfg *RetrievalConfig) { cfg.Fusion = "linear" }},
		{name: "rrfK zero", mutate: func(cfg *RetrievalConfig) { cfg.RRFK = 0 }},
		{name: "rrfK too high", mutate: func(cfg *RetrievalConfig) { cfg.RRFK = 1001 }},
		{name: "negative vector weight", mutate: func(cfg *RetrievalConfig) { cfg.VectorWeight = -0.1 }},
		{name: "both weights zero", mutate: func(cfg *RetrievalConfig) { cfg.VectorWeight, cfg.FTSWeight = 0, 0 }},
		{name: "candidate floor below topK", mutate: func(cfg *RetrievalConfig) { cfg.CandidateFloor = cfg.TopK - 1 }},
		{name: "candidate multiplier zero", mutate: func(cfg *RetrievalConfig) { cfg.CandidateMultiplier = 0 }},
		{name: "candidate max below floor", mutate: func(cfg *RetrievalConfig) { cfg.CandidateMax = cfg.CandidateFloor - 1 }},
		{name: "candidate max too high", mutate: func(cfg *RetrievalConfig) { cfg.CandidateMax = 2001 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultAgentConfig()
			test.mutate(&cfg.Retrieval)
			if err := ValidateAgentConfig(cfg); err == nil {
				t.Fatalf("expected invalid retrieval config: %#v", cfg.Retrieval)
			}
		})
	}

	valid := DefaultAgentConfig()
	valid.Retrieval.VectorWeight = 0
	if err := ValidateAgentConfig(valid); err != nil {
		t.Fatalf("one zero weight must remain valid: %v", err)
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
	for key, value := range map[string]any{
		"topK":         2.5,
		"rrfK":         "invalid",
		"vectorWeight": "NaN",
		"ftsWeight":    "invalid",
	} {
		if _, err := ParseAgentConfig(map[string]any{"retrieval": map[string]any{key: value}}); err == nil {
			t.Fatalf("expected invalid retrieval field %q to fail", key)
		}
	}
}

func TestParseAgentConfigReadsPublicTags(t *testing.T) {
	cfg, err := ParseAgentConfig(map[string]any{
		"tags": []any{"售后", "退款"},
	})
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if strings.Join(cfg.Tags, ",") != "售后,退款" {
		t.Fatalf("unexpected tags %#v", cfg.Tags)
	}
	if _, err := ParseAgentConfig(map[string]any{"tags": []any{"售后", 42}}); err == nil {
		t.Fatal("expected non-string public tag to fail")
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
		Retrieval: DefaultAgentConfig().Retrieval,
		Extraction: ExtractionConfig{
			Timeout:      time.Minute,
			MaxFileBytes: 50 * 1024 * 1024,
			PDF:          PDFExtractionConfig{Enabled: true, Backend: "poppler", Binary: "pdftotext"},
			DOCX:         DOCXExtractionConfig{Enabled: true, Backend: "native"},
			PPTX:         PPTXExtractionConfig{Enabled: true, Backend: "native", IncludeNotes: true},
		},
	}
	const want = "sha256:00ade363532c4f5d87cbc2ab8e63cf3492fb0efc412b2500d9720f342d2d4fa0"
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
