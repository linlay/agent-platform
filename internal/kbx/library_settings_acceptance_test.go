package kbx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
)

func setupLibraryBinary(t *testing.T) {
	t.Helper()
	bin := os.Getenv("KBX_KBASES_TEST_BIN")
	if bin == "" {
		t.Skip("set KBX_KBASES_TEST_BIN to managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")
	t.Setenv("no_proxy", "localhost,127.0.0.1,::1")
}

func TestLibraryRealLibraryChunkAndEncoding(t *testing.T) {
	setupLibraryBinary(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	source := filepath.Join(root, "docs")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "legacy.txt"), []byte("caf\xe9 quartzorchid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "long.md"), []byte(strings.Repeat("# Heading\nquartzorchid policy requires review before approval.\n\n", 150)), 0600); err != nil {
		t.Fatal(err)
	}
	max, overlap, zero := 900, 90, 0
	d := kbases.Definition{TextEncoding: "windows-1252", Chunk: &knowledge.ChunkSettings{Strategy: "structural", MaxChars: &max, OverlapChars: &overlap}, Collections: []kbases.Collection{{Name: "docs", SourcePath: source, Chunk: knowledge.ChunkSettings{OverlapChars: &zero}}}}
	e := NewLibraryEngine()
	db := filepath.Join(root, "index.sqlite")
	for _, strategy := range []string{"structural", "regex", "window"} {
		d.Chunk.Strategy = strategy
		if err := e.UpdateLibrary(context.Background(), db, d, nil); err != nil {
			t.Fatal(strategy, err)
		}
		raw, err := e.ReadLibrary(context.Background(), db, d, "read", "kbx://docs/legacy.txt", 0)
		if err != nil || !strings.Contains(string(raw), "café") {
			t.Fatalf("legacy encoding: %s %v", raw, err)
		}
	}
	// Unchanged bytes must be extracted again when the fallback changes.
	d.TextEncoding = "windows-1251"
	if err := e.UpdateLibrary(context.Background(), db, d, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := e.ReadLibrary(context.Background(), db, d, "read", "kbx://docs/legacy.txt", 0)
	if err != nil || !strings.Contains(string(raw), "cafй") {
		t.Fatalf("encoding change did not reextract: %s %v", raw, err)
	}
}

func TestLibraryRealLibraryModelSwitchDoesNotScanSources(t *testing.T) {
	setupLibraryBinary(t)
	var broken atomic.Bool
	var mu sync.Mutex
	seen := map[string]int{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string
			Input json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		seen[in.Model]++
		mu.Unlock()
		if broken.Load() {
			http.Error(w, "fixture failure", 400)
			return
		}
		var texts []string
		if json.Unmarshal(in.Input, &texts) != nil {
			var text string
			_ = json.Unmarshal(in.Input, &text)
			texts = []string{text}
		}
		data := []any{}
		for i := range texts {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0, 0, 0, 0, 0, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer provider.Close()
	modelSource, _ := libraryModelFixture(t, provider.URL)
	engine := NewLibraryEngineWithSource(modelSource)
	root := t.TempDir()
	libraryService, err := kbases.New(context.Background(), filepath.Join(root, "kbases"), filepath.Join(root, "ru-kbases"), engine)
	if err != nil {
		t.Fatal(err)
	}
	defer libraryService.Close(context.Background())
	wait := func(id string, degraded bool) kbases.Definition {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			d, err := libraryService.Get(id)
			if err == nil && !d.Indexing && d.State == "ready" && d.Degraded == degraded {
				return d
			}
			time.Sleep(20 * time.Millisecond)
		}
		d, _ := libraryService.Get(id)
		t.Fatalf("maintenance deadline: %+v", d)
		return d
	}
	libs := []kbases.Definition{}
	for _, key := range []string{"a", "b"} {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "doc.md"), []byte("quartzorchid original publication."), 0600); err != nil {
			t.Fatal(err)
		}
		d, err := libraryService.Create(kbases.Input{Name: key, Models: &kbases.ModelsConfig{Embedding: &kbases.EmbeddingConfig{ModelKey: key}}, Collections: []kbases.Collection{{Name: "docs", SourcePath: source}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = libraryService.Refresh(d.ID); err != nil {
			t.Fatal(err)
		}
		libs = append(libs, wait(d.ID, false))
	}
	mu.Lock()
	both := seen["model-a"] > 0 && seen["model-b"] > 0
	mu.Unlock()
	if !both {
		t.Fatal("library model selections were not isolated")
	}
	d := libs[0]
	if err := os.WriteFile(filepath.Join(d.Collections[0].SourcePath, "doc.md"), []byte("neverindexed replacement."), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = libraryService.Edit(d.ID, kbases.Input{Name: d.Name, Models: &kbases.ModelsConfig{Embedding: &kbases.EmbeddingConfig{ModelKey: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = libraryService.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	wait(d.ID, false)
	raw, err := libraryService.Read(context.Background(), d.ID, "search", "quartzorchid", 10)
	if err != nil || !strings.Contains(string(raw), "original publication") {
		t.Fatalf("vector switch rescanned sources: %s %v", raw, err)
	}
	broken.Store(true)
	if _, err = libraryService.Edit(d.ID, kbases.Input{Name: d.Name, Models: &kbases.ModelsConfig{Embedding: &kbases.EmbeddingConfig{ModelKey: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = libraryService.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	wait(d.ID, true)
	if _, err = libraryService.Read(context.Background(), d.ID, "search", "quartzorchid", 10); err != nil {
		t.Fatal("full text lost after vector failure", err)
	}
	if _, err = libraryService.Read(context.Background(), d.ID, "vsearch", "quartzorchid", 10); err == nil {
		t.Fatal("strict vector read accepted after failed model switch")
	}
	broken.Store(false)
	if _, err = libraryService.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	wait(d.ID, false)
	if _, err = libraryService.Read(context.Background(), d.ID, "vsearch", "quartzorchid", 10); err != nil {
		t.Fatal("vector recovery failed", err)
	}
}
