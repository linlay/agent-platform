package kbx

import (
	"agent-platform/internal/builtins"
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestLibraryDisplaysSimilarityWithoutChangingRank(t *testing.T) {
	e := NewLibraryEngine()
	e.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "--explain") {
			t.Fatal("original vector scores were not requested")
		}
		return responseJSON(map[string]any{"results": []any{
			map[string]any{"file": "kbx://docs/a.md", "score": 1, "resultId": "first", "explain": map[string]any{"rrf": map[string]any{"contributions": []any{map[string]any{"source": "vec", "queryType": "original", "backendScore": 0.3819310665130615}}}}},
			map[string]any{"file": "kbx://docs/b.md", "score": 0.5, "resultId": "second", "explain": map[string]any{"rrf": map[string]any{"contributions": []any{map[string]any{"source": "vec", "queryType": "original", "backendScore": 0.7}}}}},
		}}), nil
	})
	raw, err := e.Read(context.Background(), "unused", "query", "fixture", 2, "docs")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Results []struct {
			ResultID, ScoreType string
			Score, RankingScore float64
		}
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 || result.Results[0].ResultID != "first" || result.Results[1].ResultID != "second" || result.Results[0].Score != 0.3819310665130615 || result.Results[0].RankingScore != 1 || result.Results[0].ScoreType != "vector_similarity" {
		t.Fatalf("wrong score/order: %s", raw)
	}
}
func TestLibrarySimilarityAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, method, row string
		want              *float64
	}{
		{"lexical", "search", `{"score":0.9}`, nil},
		{"graph", "gsearch", `{"score":1}`, nil},
		{"no explanation", "query", `{"score":1}`, nil},
		{"expanded query only", "query", `{"score":1,"explain":{"rrf":{"contributions":[{"source":"vec","queryType":"expanded","backendScore":0.8}]}}}`, nil},
		{"zero", "query", `{"score":1,"explain":{"rrf":{"contributions":[{"source":"vec","queryType":"original","backendScore":0}]}}}`, ptrScore(0)},
		{"negative vector", "vsearch", `{"score":-0.15}`, ptrScore(-0.15)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := withSimilarityScores(json.RawMessage(`{"results":[`+tc.row+`]}`), tc.method)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Results []struct {
					Score     *float64
					ScoreType string
				}
			}
			_ = json.Unmarshal(raw, &result)
			got := result.Results[0]
			if tc.want == nil {
				if got.Score != nil || got.ScoreType != "unavailable" {
					t.Fatalf("invented similarity: %s", raw)
				}
			} else if got.Score == nil || *got.Score != *tc.want || got.ScoreType != "vector_similarity" {
				t.Fatalf("lost similarity: %s", raw)
			}
		})
	}
}
func ptrScore(v float64) *float64 { return &v }

// Opt-in read-only verification against an existing library using its deployed
// configuration. This performs queries only, never update/embed or source writes.
func TestLiveLibrarySimilarity(t *testing.T) {
	bin, db, cfg := os.Getenv("KBX_SCORE_TEST_BIN"), os.Getenv("KBX_SCORE_TEST_DB"), os.Getenv("KBX_SCORE_TEST_CONFIG")
	if bin == "" || db == "" || cfg == "" {
		t.Skip("set KBX_SCORE_TEST_BIN/DB/CONFIG")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	e := NewLibraryEngine()
	e.runner = cliRunner{configFile: cfg}
	for _, q := range []string{"曾万元最近的工作是什么", "曾念美负责了什么", "张倩做了什么事情"} {
		raw, err := e.Read(context.Background(), db, "query", q, 5, "AI", "shuzhi")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Results []struct {
				Score        *float64
				ScoreType    string
				RankingScore float64
				Explain      struct {
					RRF struct {
						Contributions []struct {
							Source, QueryType string
							BackendScore      float64
						}
					}
				}
			}
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Results) == 0 {
			t.Fatalf("no results for %s", q)
		}
		first := result.Results[0]
		if first.Score == nil || first.ScoreType != "vector_similarity" {
			t.Fatalf("missing similarity for %s", q)
		}
		found := false
		for _, c := range first.Explain.RRF.Contributions {
			if c.Source == "vec" && c.QueryType == "original" {
				found = true
				if *first.Score != c.BackendScore {
					t.Fatal("score differs from original query vector similarity")
				}
			}
		}
		if !found {
			t.Fatal("missing underlying vector evidence")
		}
		t.Logf("%s: similarity=%.8f, original ranking score=%.2f", q, *first.Score, first.RankingScore)
	}
}
