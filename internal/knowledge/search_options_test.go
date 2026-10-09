package knowledge

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func TestSearchToolDecodesAdvancedOptions(t *testing.T) {
	var args map[string]any
	if err := json.Unmarshal([]byte(`{"query":"policy","method":"query","limit":5,"candidateLimit":80,"exclude":["legacy","deprecated"],"intent":"deployment","filter":"project = payments and owner exists","minScore":0,"recencyWeight":0.25,"recencyHalfLifeDays":30,"noGraph":"true"}`), &args); err != nil {
		t.Fatal(err)
	}
	s := &stubToolService{searchResult: SearchResult{Engine: "kbx", Method: "query"}}
	r, err := NewToolHandler(s).Invoke(context.Background(), ToolSearch, args, kbaseToolExecutionContext())
	o := s.searchOptions
	if err != nil || r.Error != "" || r.Structured["method"] != "query" || o.Method != "query" || o.CandidateLimit != 80 || len(o.Exclude) != 2 || o.Intent != "deployment" || o.Filter != "project = payments and owner exists" || o.MinScore == nil || *o.MinScore != 0 || o.RecencyWeight == nil || *o.RecencyWeight != .25 || o.RecencyHalfLifeDays == nil || *o.RecencyHalfLifeDays != 30 || !o.NoGraph {
		t.Fatalf("result=%+v options=%+v err=%v", r, o, err)
	}
	if args["noGraph"] != "true" {
		t.Fatal("input was mutated")
	}
	_, err = NewToolHandler(s).Invoke(context.Background(), ToolSearch, map[string]any{"query": "dependencies", "method": "gsearch", "entities": []any{"Payment"}, "relations": []any{"depends_on"}, "direction": "out", "maxHops": float64(3)}, kbaseToolExecutionContext())
	if err != nil || len(s.searchOptions.Entities) != 1 || len(s.searchOptions.Relations) != 1 || s.searchOptions.Direction != "out" || s.searchOptions.MaxHops != 3 {
		t.Fatalf("graph options=%+v err=%v", s.searchOptions, err)
	}
}

func TestSearchToolRejectsInvalidParametersBeforeService(t *testing.T) {
	for _, raw := range []string{
		`{"agentKey":"other-agent"}`, `{"libraryId":"other-library"}`, `{"Method":"gsearch"}`, `{"min_score":0.8}`, `{"method":null}`, `{"exclude":null}`,
		`{"method":"unknown"}`, `{"method":4}`, `{"limit":1.5}`, `{"limit":51}`, `{"offset":1}`,
		`{"exclude":"legacy"}`, `{"exclude":[42]}`, `{"exclude":[""]}`,
		`{"filter":{}}`, `{"minScore":"0.1"}`, `{"noGraph":"TRUE"}`,
		`{"recencyWeight":1.1}`, `{"recencyHalfLifeDays":0}`, `{"candidateLimit":2001}`, `{"candidateLimit":0}`, `{"method":"gsearch","maxHops":0}`,
		`{"method":"search","noGraph":true}`, `{"method":"query","entities":["Payment"]}`,
		`{"method":"gsearch","exclude":["old"]}`, `{"method":"gsearch","intent":"x"}`,
		`{"method":"gsearch","minScore":0}`, `{"method":"gsearch","candidateLimit":10}`,
		`{"method":"gsearch","recencyWeight":0}`, `{"method":"gsearch","direction":"wrong"}`,
		`{"method":"gsearch","maxHops":4}`, `{"method":"gsearch","relations":[" "]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				t.Fatal(err)
			}
			args["query"] = "policy"
			s := &stubToolService{}
			r, err := NewToolHandler(s).Invoke(context.Background(), ToolSearch, args, kbaseToolExecutionContext())
			if err != nil || r.Error != "kbase_invalid_request" || s.agentKey != "" {
				t.Fatalf("result=%+v service called=%s err=%v", r, s.agentKey, err)
			}
		})
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := NormalizeSearchOptions(SearchOptions{MinScore: &value}); err == nil {
			t.Fatal("non-finite score accepted")
		}
	}
}

func TestGraphPublicationIncludesSupportingDocuments(t *testing.T) {
	sources := searchHitSources([]SearchHit{{
		ChunkID: "whole-chunk", EvidenceID: "first-passage", Path: "a.md", Snippet: "first", MatchType: "graph",
		Graph: &GraphExplanation{BestPath: &GraphPath{Score: .8, Edges: []GraphEdge{{
			Predicate: "depends_on", Evidence: []GraphEvidence{
				{Path: "a.md", EvidenceID: "first-passage", Content: "first"},
				{Path: "b.md", EvidenceID: "second-passage", StartLine: 3, EndLine: 4, Content: "second"},
			},
		}}}},
	}})
	if len(sources) != 2 || len(sources[0].Chunks) != 1 || sources[0].Chunks[0].ChunkID != "first-passage" || sources[1].Chunks[0].ChunkID != "second-passage" || sources[1].Chunks[0].StartLine != 3 {
		t.Fatalf("graph sources lost or duplicated: %+v", sources)
	}
}
