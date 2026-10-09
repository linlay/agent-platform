package kbx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

func TestLiveSharedLibraryLifecycle(t *testing.T) {
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")
	t.Setenv("no_proxy", "localhost,127.0.0.1,::1")
	var broken, leaked atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input json.RawMessage }
		json.NewDecoder(r.Body).Decode(&in)
		if strings.Contains(string(in.Input), "EXCLUDED_SECRET") {
			leaked.Store(true)
		}
		if broken.Load() {
			http.Error(w, "fixture failure", 400)
			return
		}
		var texts []string
		if json.Unmarshal(in.Input, &texts) != nil {
			var text string
			json.Unmarshal(in.Input, &text)
			texts = []string{text}
		}
		data := []any{}
		for i := range texts {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0, 0, 0, 0, 0, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer provider.Close()
	root := t.TempDir()
	for _, dir := range []string{"models", "providers"} {
		os.Mkdir(filepath.Join(root, dir), 0700)
	}
	os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte("key: test\nbaseUrl: "+provider.URL+"\n"), 0600)
	os.WriteFile(filepath.Join(root, "models", "embed.yml"), []byte("key: embed\nprovider: test\ntype: embedding\nmodelId: fixture\nembedding:\n  dimension: 8\n  endpointPath: /v1/embeddings\n"), 0600)
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	modelSource := &ModelConfigSource{File: filepath.Join(root, "state", "index.yml"), Registry: registry, ModelKey: "embed"}
	engine := NewCenterEngineWithSource(modelSource)
	center, err := kbasescenter.New(context.Background(), filepath.Join(root, "kbases"), filepath.Join(root, "ru-kbases"), engine, kbasescenter.Options{Debounce: 30 * time.Millisecond, ReconcileInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer center.Close(context.Background())
	first, second := t.TempDir(), t.TempDir()
	for _, p := range []string{first, second} {
		os.WriteFile(filepath.Join(p, "same.md"), []byte("Orchard approval requires two reviewers."), 0600)
		os.Mkdir(filepath.Join(p, "private"), 0700)
		os.WriteFile(filepath.Join(p, "private", "secret.md"), []byte("EXCLUDED_SECRET"), 0600)
	}
	d, err := center.Create(kbasescenter.Input{Name: "shared", Collections: []kbasescenter.Collection{{Name: "ai", SourcePath: first, Exclude: []string{"private/**"}}, {Name: "research", SourcePath: second, Exclude: []string{"private/**"}}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := knowledge.DefaultConfig()
	cfg.Enabled = true
	cfg.LibraryID = d.ID
	m := NewManager(Options{Center: center, ConfigSource: modelSource}, testSource{"docs": {Key: "docs", WorkspaceRoot: t.TempDir(), Config: cfg}}, nil)
	if err = center.Start(); err != nil {
		t.Fatal(err)
	}
	wait := func(check func(kbasescenter.Definition) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			state, e := center.Get(d.ID)
			if e == nil && check(state) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		state, _ := center.Get(d.ID)
		t.Fatalf("deadline: %+v", state)
	}
	wait(func(s kbasescenter.Definition) bool { return s.State == "ready" && !s.Degraded })
	hits, err := m.Search(context.Background(), "docs", "Orchard", knowledge.SearchOptions{Method: "search"})
	if err != nil || len(hits.Results) != 2 {
		t.Fatalf("multi collection: %+v %v", hits, err)
	}
	if hits.Results[0].Path == hits.Results[1].Path {
		t.Fatal("same-named sources merged")
	}
	broken.Store(true)
	os.WriteFile(filepath.Join(second, "same.md"), []byte("Orchard now requires three reviewers."), 0600)
	wait(func(s kbasescenter.Definition) bool { return s.State == "ready" && s.Degraded })
	hits, err = m.Search(context.Background(), "docs", "three", knowledge.SearchOptions{Method: "search", PathPrefix: "research"})
	if err != nil || len(hits.Results) != 1 {
		t.Fatalf("text after embedding failure: %+v %v", hits, err)
	}
	broken.Store(false)
	if _, err = center.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	wait(func(s kbasescenter.Definition) bool { return s.State == "ready" && !s.Degraded })
	os.Remove(filepath.Join(second, "same.md"))
	wait(func(s kbasescenter.Definition) bool {
		files, e := m.Files("docs", knowledge.FilesOptions{HeadLimit: 50})
		return e == nil && !s.Indexing && len(files.Results) == 1
	})
	// Rechunk unchanged source bytes after a configuration-only update.
	os.WriteFile(filepath.Join(first, "same.md"), []byte(strings.Repeat("Orchard policy requires review before approval. ", 180)), 0600)
	wait(func(s kbasescenter.Definition) bool {
		hits, e := m.Search(context.Background(), "docs", "Orchard", knowledge.SearchOptions{Method: "search", Limit: 50})
		return e == nil && !s.Indexing && len(hits.Results) > 1
	})
	before, err := m.Search(context.Background(), "docs", "Orchard", knowledge.SearchOptions{Method: "search", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	d, err = center.Get(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastIndexed := d.IndexedAt
	d.Collections[0].Chunk = knowledge.ChunkConfig{Unit: "chars", MaxChars: 800, OverlapChars: 80}
	if _, err = center.Edit(d.ID, kbasescenter.Input{Name: d.Name, Collections: d.Collections}); err != nil {
		t.Fatal(err)
	}
	if err = m.ValidateRun("docs"); err == nil {
		t.Fatal("changed scope remained readable")
	}
	wait(func(s kbasescenter.Definition) bool {
		return s.State == "ready" && !s.Degraded && s.IndexedAt > lastIndexed
	})
	after, err := m.Search(context.Background(), "docs", "Orchard", knowledge.SearchOptions{Method: "search", Limit: 50})
	if err != nil || len(after.Results) <= len(before.Results) {
		t.Fatalf("configuration-only rechunk did not publish: before=%d after=%d error=%v", len(before.Results), len(after.Results), err)
	}
	if leaked.Load() {
		t.Fatal("excluded document reached embedding")
	}
}
