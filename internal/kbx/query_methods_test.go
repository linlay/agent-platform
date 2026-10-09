package kbx

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/knowledge"
)

func searchFixture() map[string]any {
	return map[string]any{
		"file": "kbx://workspace/docs/a.md", "title": "Architecture", "score": .8,
		"chunk":    map[string]any{"id": locator, "range": map[string]int{"lineStart": 1, "lineEnd": 4}},
		"evidence": map[string]any{"id": locator, "text": "Payment depends on Orders", "range": map[string]int{"lineStart": 2, "lineEnd": 2}},
	}
}

func searchFixtureResponse(method string, hit map[string]any) []byte {
	if method == "gsearch" {
		return responseJSON([]any{hit})
	}
	return responseJSON(map[string]any{"type": "kbx.search.response", "retrievalVersion": 6, "results": []any{hit}, "trace": map[string]any{"resultUnit": "chunk", "coverage": map[string]any{"retrievalUsed": []string{method}}}})
}

func graphFixture(file string) map[string]any {
	return map[string]any{"graph": map[string]any{
		"links":           []any{map[string]any{"name": "Payment", "entityKey": "payment", "type": "system"}},
		"supportingPaths": 1, "score": map[string]any{"total": .8},
		"bestPath": map[string]any{"score": .8, "nodes": []any{map[string]any{"key": "payment", "name": "Payment", "type": "system"}, map[string]any{"key": "orders", "name": "Orders", "type": "system"}}, "edges": []any{map[string]any{"predicate": "depends_on", "confidence": .9, "evidence": []any{map[string]any{"file": file, "evidence": searchFixture()["evidence"]}}}}},
	}}
}

func TestSearchMethodsRouteAndKeepPolicy(t *testing.T) {
	for _, method := range []string{"query", "search", "vsearch", "gsearch"} {
		t.Run(method, func(t *testing.T) {
			m, _ := newTestManager(t)
			score, weight, days := .1, .25, 30.0
			o := knowledge.SearchOptions{Method: method, Filter: "project = payments or ext = md", PathPrefix: "docs", Limit: 5}
			if method == "gsearch" {
				o.Entities, o.Relations, o.Direction, o.MaxHops = []string{"--Payment"}, []string{"depends_on"}, "out", 3
			} else {
				o.Exclude, o.Intent, o.MinScore = []string{"--legacy", "deprecated"}, "--deployment", &score
				o.CandidateLimit, o.RecencyWeight, o.RecencyHalfLifeDays = 80, &weight, &days
			}
			m.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
				if args[0] != method || args[len(args)-2] != "--" || args[len(args)-1] != "--query" {
					t.Fatalf("wrong command or unprotected query: %v", args)
				}
				joined := strings.Join(args, " ")
				for _, part := range []string{"-c workspace", "--filter=project = payments or ext = md", `"op":"pathPrefix"`, `.kbx-platform/**`} {
					if !strings.Contains(joined, part) {
						t.Fatalf("missing %q: %s", part, joined)
					}
				}
				if method == "gsearch" {
					for _, arg := range []string{"--full", "-C", "--no-rerank", "--no-graph"} {
						if slices.Contains(args, arg) {
							t.Fatalf("graph received incompatible flag %s", arg)
						}
					}
					for _, arg := range []string{"--explain", "--entity=--Payment", "--relation=depends_on", "out", "3"} {
						if !slices.Contains(args, arg) {
							t.Fatalf("graph missing %s: %v", arg, args)
						}
					}
				} else {
					for _, arg := range []string{"--full", "-C", "80", "--exclude=--legacy", "--exclude=deprecated", "--intent=--deployment", "--min-score=0.1", "--recency-weight=0.25", "--recency-half-life-days=30"} {
						if !slices.Contains(args, arg) {
							t.Fatalf("missing %s: %v", arg, args)
						}
					}
					if slices.Contains(args, "--no-rerank") != (method == "query") || slices.Contains(args, "--no-graph") {
						t.Fatalf("wrong hybrid flags: %v", args)
					}
				}
				return searchFixtureResponse(method, searchFixture()), nil
			})
			r, err := m.Search(context.Background(), "docs", "--query", o)
			if err != nil || r.Method != method || len(r.Results) != 1 {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestGraphEvidenceSurvivesToolAndRejectsScopeEscape(t *testing.T) {
	for _, file := range []string{"kbx://workspace/docs/a.md", "kbx://other/secret.md", "kbx://workspace/../secret.md", "kbx://workspace/.kbx-platform/secret.md"} {
		t.Run(file, func(t *testing.T) {
			m, _ := newTestManager(t)
			m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
				fixture := searchFixture()
				fixture["explain"] = graphFixture(file)
				return searchFixtureResponse("gsearch", fixture), nil
			})
			r, err := knowledge.NewToolHandler(m).Invoke(context.Background(), knowledge.ToolSearch, map[string]any{"query": "Payment", "method": "gsearch"}, &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", KBaseEnabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			if file != "kbx://workspace/docs/a.md" {
				if r.Error == "" || r.SourcePublication != nil {
					t.Fatalf("escaped graph evidence: %+v", r)
				}
				return
			}
			if r.Error != "" || r.SourcePublication == nil {
				t.Fatalf("%+v", r)
			}
			hit := r.Structured["results"].([]knowledge.SearchHit)[0]
			if hit.StartLine != 2 || hit.EndLine != 2 || hit.Graph == nil || hit.Graph.BestPath == nil || len(hit.Graph.BestPath.Edges) != 1 {
				t.Fatalf("lost graph or evidence range: %+v", hit)
			}
			evidence := hit.Graph.BestPath.Edges[0].Evidence[0]
			if evidence.Path != "docs/a.md" || evidence.EvidenceID != locator || evidence.Content != "Payment depends on Orders" {
				t.Fatalf("lost graph evidence: %+v", evidence)
			}
			if _, ok := r.Structured["candidateBudgetExhausted"]; ok {
				t.Fatal("graph response invented candidate budget information")
			}
		})
	}
}

func TestLocalSearchDoesNotResolveModelAndHybridCanDisableGraph(t *testing.T) {
	for _, method := range []string{"search", "gsearch"} {
		m, _ := newTestManager(t)
		m.options.ConfigSource = &ModelConfigSource{File: filepath.Join(t.TempDir(), "config.yml"), ModelKey: "missing"}
		m.runner = runFunc(func(_ context.Context, _ string, cfg []byte, _ ...string) ([]byte, error) {
			if !strings.Contains(string(cfg), `"embedding":null`) {
				t.Fatalf("local search received model configuration: %s", cfg)
			}
			return searchFixtureResponse(method, searchFixture()), nil
		})
		if _, err := m.Search(context.Background(), "docs", "Payment", knowledge.SearchOptions{Method: method}); err != nil {
			t.Fatalf("local %s depended on model config: %v", method, err)
		}
	}
	m, _ := newTestManager(t)
	m.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "--no-graph") {
			t.Fatal("noGraph lost")
		}
		return searchFixtureResponse("query", searchFixture()), nil
	})
	if _, err := m.Search(context.Background(), "docs", "Payment", knowledge.SearchOptions{NoGraph: true}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFailureRetainsSafeCodeAndNeverFallsBack(t *testing.T) {
	for _, method := range []string{"vsearch", "gsearch"} {
		m, _ := newTestManager(t)
		calls := 0
		m.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
			calls++
			if args[0] != method {
				t.Fatal("silently changed retrieval method")
			}
			raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "type": "kbx.agent.response", "status": "error", "error": map[string]any{"code": "EXECUTION_FAILED", "message": "private-provider-token", "hint": "private-provider-url"}})
			return raw, errors.New("exit status 1")
		})
		_, err := m.Search(context.Background(), "docs", "Payment", knowledge.SearchOptions{Method: method})
		if knowledge.KindOf(err) != knowledge.ErrorUnavailable || !strings.Contains(err.Error(), "EXECUTION_FAILED") || strings.Contains(err.Error(), "private-provider") || calls != 1 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	}
}

func TestSearchRejectsBudgetOutsideAgentLimits(t *testing.T) {
	m, _ := newTestManager(t)
	m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
		t.Fatal("invalid budget invoked the CLI")
		return nil, nil
	})
	for _, candidate := range []int{1, 501} {
		if _, err := m.Search(context.Background(), "docs", "policy", knowledge.SearchOptions{CandidateLimit: candidate}); err == nil {
			t.Fatalf("accepted candidate budget %d", candidate)
		}
	}
}
