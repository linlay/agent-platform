package kbx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
)

func TestLibraryRealRetrievalModelsDefaultsAndDegradation(t *testing.T) {
	setupLibraryBinary(t)
	var expansions, ranks atomic.Int32
	var broken, leaked atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model     string
			Documents []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			expansions.Add(1)
			if in.Model != "expand-fixture" {
				t.Errorf("wrong expansion model: %s", in.Model)
			}
			if broken.Load() {
				http.Error(w, "fixture failure", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "lex: quartzorchid"}}}})
		} else if r.URL.Path == "/v1/rerank" {
			ranks.Add(1)
			if in.Model != "rank-fixture" {
				t.Errorf("wrong rerank model: %s", in.Model)
			}
			for _, doc := range in.Documents {
				if strings.Contains(doc, "EXCLUDED_SECRET") {
					leaked.Store(true)
				}
			}
			if broken.Load() {
				http.Error(w, "fixture failure", 400)
				return
			}
			results := []any{}
			for i := range in.Documents {
				results = append(results, map[string]any{"index": i, "relevance_score": .95 - float64(i)*.05})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		} else {
			t.Errorf("unexpected model endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	source, providerFile := libraryModelFixture(t, provider.URL)
	source.ModelKey = ""
	root := filepath.Dir(filepath.Dir(providerFile))
	for file, body := range map[string]string{
		"rank.yml":   "key: rank\nprovider: fixture\ntype: reranker\nmodelId: rank-fixture\ntimeout: 1\nreranker:\n  endpointPath: /v1/rerank\n",
		"expand.yml": "key: expand\nprovider: fixture\ntype: chat\nprotocol: OPENAI\nmodelId: expand-fixture\ntimeout: 1\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "models", file), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Registry.ReloadModels(); err != nil {
		t.Fatal(err)
	}
	engine := NewLibraryEngineWithSource(source)
	libraryService, err := kbases.New(context.Background(), filepath.Join(root, "kbases"), filepath.Join(root, "ru-kbases"), engine)
	if err != nil {
		t.Fatal(err)
	}
	defer libraryService.Close(context.Background())
	yes, no, top, floor := true, false, 2, 20
	collections := []kbases.Collection{}
	for _, name := range []string{"docs", "hidden"} {
		dir := t.TempDir()
		body := "quartzorchid original policy."
		if name == "hidden" {
			body = "quartzorchid EXCLUDED_SECRET"
		}
		if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		c := kbases.Collection{Name: name, SourcePath: dir}
		if name == "hidden" {
			c.DefaultQuery = &no
		}
		collections = append(collections, c)
	}
	d, err := libraryService.Create(kbases.Input{Name: "retrieval", Collections: collections, Retrieval: &knowledge.RetrievalSettings{TopK: &top, CandidateFloor: &floor, Rerank: &yes, QueryExpansion: &yes}, Models: &kbases.ModelsConfig{Reranker: &kbases.QueryModelConfig{ModelKey: "rank"}, QueryExpansion: &kbases.QueryModelConfig{ModelKey: "expand"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = libraryService.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		d, err = libraryService.Get(d.ID)
		if err == nil && d.State == "ready" && !d.Indexing {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.State != "ready" || d.Indexing {
		t.Fatalf("not ready: %+v", d)
	}
	if expansions.Load() != 0 || ranks.Load() != 0 {
		t.Fatal("maintenance called retrieval models")
	}
	cfg, err := knowledge.ParseConfig(map[string]any{"libraryId": d.ID})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{KBases: libraryService, ConfigSource: source}, testSource{"agent": {Key: "agent", Config: cfg}}, nil)
	start := time.Now()
	result, err := m.Search(context.Background(), "agent", "quartzorchid", knowledge.SearchOptions{NoGraph: true})
	if err != nil || result.Limit != 2 || len(result.Results) != 1 || result.Results[0].Path != "docs/a.md" || expansions.Load() != 1 || ranks.Load() != 1 || leaked.Load() {
		t.Fatalf("retrieval pipeline: %+v %v calls=%d/%d leaked=%v", result, err, expansions.Load(), ranks.Load(), leaked.Load())
	}
	t.Logf("local fixture query with expansion and rerank: %s", time.Since(start))
	// Pure full-text and explicitly disabled query roles perform no model I/O.
	for _, options := range []knowledge.SearchOptions{{Method: "search"}, {Method: "query", Rerank: &no, QueryExpansion: &no, NoGraph: true}} {
		if _, err = m.Search(context.Background(), "agent", "quartzorchid", options); err != nil {
			t.Fatal(err)
		}
	}
	if expansions.Load() != 1 || ranks.Load() != 1 {
		t.Fatal("disabled models were called")
	}
	hidden, err := m.Search(context.Background(), "agent", "quartzorchid", knowledge.SearchOptions{Method: "search", Collections: []string{"hidden"}})
	if err != nil || len(hidden.Results) != 1 || hidden.Results[0].Path != "hidden/a.md" {
		t.Fatalf("explicit collection unavailable: %+v %v", hidden, err)
	}
	broken.Store(true)
	start = time.Now()
	failed, err := m.Search(context.Background(), "agent", "quartzorchid", knowledge.SearchOptions{NoGraph: true, Intent: "recovery"})
	if err != nil || len(failed.Results) == 0 || !failed.Degraded || !slices.Contains(failed.OptionalUnavailable, "query_expansion") || !slices.Contains(failed.OptionalUnavailable, "reranker") {
		t.Fatalf("optional failure lost fallback/trace: %+v %v", failed, err)
	}
	t.Logf("local fixture query with two HTTP failures: %s", time.Since(start))
	// Metadata changes are immediate and keep the previously committed index.
	before := d.IndexedAt
	for i := range d.Collections {
		d.Collections[i].DefaultQuery = &no
	}
	edited, err := libraryService.Edit(d.ID, kbases.Input{Name: d.Name, Collections: d.Collections})
	if err != nil || edited.IndexedAt != before {
		t.Fatalf("default selection rebuilt index: %+v %v", edited, err)
	}
	raw, err := libraryService.Search(context.Background(), d.ID, kbases.SearchInput{Query: "quartzorchid"})
	if err != nil || !strings.Contains(string(raw), `"results":[]`) {
		t.Fatalf("admin empty defaults widened: %s %v", raw, err)
	}
	empty, err := m.Search(context.Background(), "agent", "quartzorchid", knowledge.SearchOptions{})
	if err != nil || len(empty.Results) != 0 {
		t.Fatalf("Agent empty defaults widened: %+v %v", empty, err)
	}
}
