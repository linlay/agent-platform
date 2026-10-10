package kbx

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/knowledge"
)

func TestQueryDefaultsAndCollectionSelection(t *testing.T) {
	m, l := newTestManager(t)
	d, err := m.options.Center.Get(l.definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled, top, score := false, 12, .4
	d.Collections[0].DefaultQuery = &disabled
	if _, err = m.options.Center.Edit(d.ID, kbasescenter.Input{Name: d.Name, Collections: d.Collections, Retrieval: &knowledge.RetrievalSettings{TopK: &top, MinScore: &score}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.runner = runFunc(func(_ context.Context, _ string, cfg []byte, args ...string) ([]byte, error) {
		calls++
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-n 12") || !strings.Contains(joined, "--min-score=0.4") || !strings.Contains(joined, "-c workspace") {
			t.Fatalf("defaults not applied: %s", joined)
		}
		return searchFixtureResponse("search", searchFixture()), nil
	})
	r, err := m.Search(context.Background(), "docs", "fixture", knowledge.SearchOptions{Method: "search"})
	if err != nil || len(r.Results) != 0 || calls != 0 {
		t.Fatalf("empty defaults searched all: %+v %v calls=%d", r, err, calls)
	}
	if _, err = m.Search(context.Background(), "docs", "fixture", knowledge.SearchOptions{Method: "search", Collections: []string{"workspace"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("explicit excluded collection not searched")
	}
	if _, err = m.Search(context.Background(), "docs", "fixture", knowledge.SearchOptions{Method: "search", Collections: []string{"outside"}}); err == nil {
		t.Fatal("scope widened")
	}
}
func TestQueryModelConfigNeverAffectsMaintenanceOrVectorFingerprint(t *testing.T) {
	source, _ := libraryModelFixture(t, "https://example.test")
	e := NewCenterEngineWithSource(source)
	d := kbasescenter.Definition{}
	fp := e.VectorFingerprint(d)
	d.Models = &kbasescenter.ModelsConfig{Reranker: &kbasescenter.QueryModelConfig{ModelKey: "unavailable"}, QueryExpansion: &kbasescenter.QueryModelConfig{ModelKey: "unavailable"}}
	if fp != e.VectorFingerprint(d) {
		t.Fatal("query role changed vector fingerprint")
	}
	raw, err := e.libraryConfig(d, true)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	if cfg["models"].(map[string]any)["reranker"] != nil {
		t.Fatal("query model leaked to maintenance")
	}
	m := NewManager(Options{ConfigSource: source}, nil, nil)
	if _, err = m.queryConfig(raw, d, "search", nil); err != nil {
		t.Fatal("pure text resolved models", err)
	}
	if _, err = m.queryConfig(raw, d, "query", nil); err == nil {
		t.Fatal("missing query model accepted")
	}
	no := false
	if _, err = m.queryConfig(raw, d, "query", &knowledge.SearchOptions{Rerank: &no, QueryExpansion: &no}); err != nil {
		t.Fatal("disabled query role still resolved", err)
	}
}
