package knowledge

import "testing"

func TestRetrievalInheritancePreservesExplicitDefaultsAndZero(t *testing.T) {
	top, floor, cap, score, weight := 12, 60, 200, .7, .3
	library := &RetrievalSettings{TopK: &top, CandidateFloor: &floor, CandidateMax: &cap, MinScore: &score, RecencyWeight: &weight}
	agent, err := ParseConfig(map[string]any{"libraryId": "docs", "retrieval": map[string]any{"topK": 8, "minScore": 0, "rerank": false}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := MergeRetrieval(library, agent)
	if err != nil || got.TopK != 8 || got.CandidateFloor != 60 || got.CandidateMax != 200 || *got.MinScore != 0 || *got.RecencyWeight != .3 || *got.Rerank {
		t.Fatalf("merge: %+v %v", got, err)
	}
	zero, enabled := 0.0, true
	o := got.ApplySearchDefaults(SearchOptions{Method: "query", Limit: 3, MinScore: &zero, RecencyWeight: &zero, Rerank: &enabled})
	if o.Limit != 3 || *o.RecencyWeight != 0 || !*o.Rerank {
		t.Fatalf("call overrides: %+v", o)
	}
	empty, err := ParseConfig(map[string]any{"libraryId": "docs"})
	if err != nil {
		t.Fatal(err)
	}
	inherited, err := MergeRetrieval(library, empty)
	if err != nil || inherited.TopK != 12 {
		t.Fatalf("omitted Agent field defeated library: %+v %v", inherited, err)
	}
	graph := got.ApplySearchDefaults(SearchOptions{Method: "gsearch"})
	if _, err = NormalizeSearchOptions(graph); err != nil {
		t.Fatal("inapplicable inherited defaults reached graph", err)
	}
}
func TestQueryControlsValidateBeforeDispatch(t *testing.T) {
	for _, args := range []map[string]any{
		{"rerank": "TRUE"}, {"method": "search", "rerank": true}, {"method": "gsearch", "queryExpansion": false}, {"collections": []any{}}, {"collections": []any{""}}, {"recencyWeight": 2},
	} {
		if _, err := searchOptionsFromArgs(args); err == nil {
			t.Fatal("accepted invalid controls", args)
		}
	}
	got, err := searchOptionsFromArgs(map[string]any{"rerank": "false", "queryExpansion": "true", "collections": []any{"docs"}})
	if err != nil || *got.Rerank || !*got.QueryExpansion || len(got.Collections) != 1 {
		t.Fatalf("boolean normalization: %+v %v", got, err)
	}
}
