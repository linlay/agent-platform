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
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/knowledge"
)

func liveSearchManager(t *testing.T, files map[string]string) (*Manager, library, context.Context) {
	t.Helper()
	bin := os.Getenv("KBX_ACCEPTANCE_BIN")
	if bin == "" {
		t.Skip("set KBX_ACCEPTANCE_BIN to the current managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	m, l := newTestManager(t)
	if err := os.Remove(l.database); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		file := filepath.Join(l.spec.WorkspaceRoot, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	cfg, err := m.config(l, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.runner.Run(ctx, l.database, cfg, "collection", "add", l.spec.WorkspaceRoot, "--name", "workspace"); err != nil {
		t.Fatal(err)
	}
	return m, l, ctx
}

func TestLiveSearchMethodsAndAdvancedFilters(t *testing.T) {
	m, _, ctx := liveSearchManager(t, map[string]string{
		"docs/current.md": "OrchardFault rollback uses the current deployment guide.",
		"docs/old.md":     "OrchardFault deprecated legacy deployment procedure.",
		"other/a.md":      "OrchardFault unrelated system.",
	})
	zero, weight, days := 0.0, .1, 30.0
	for _, method := range []string{"search", "query"} {
		r, err := m.Search(ctx, "docs", "OrchardFault", knowledge.SearchOptions{
			Method: method, Limit: 5, CandidateLimit: 40, PathPrefix: "docs",
			Filter: "ext = md and sys.size > 0", Exclude: []string{"deprecated"}, Intent: "deployment",
			MinScore: &zero, RecencyWeight: &weight, RecencyHalfLifeDays: &days,
		})
		if err != nil || len(r.Results) != 1 || r.Results[0].Path != "docs/current.md" {
			t.Fatalf("%s filtered search: %+v %v", method, r, err)
		}
		read, err := m.Read("docs", knowledge.ReadOptions{ChunkID: r.Results[0].EvidenceID})
		if err != nil || read.Content != r.Results[0].Snippet {
			t.Fatalf("%s evidence: %+v %v", method, read, err)
		}
		if method == "search" && (r.Degraded || slices.Contains(r.RetrievalChannels, "vector")) {
			t.Fatalf("full-text search used optional retrieval: %+v", r)
		}
	}
	high := 1e12
	r, err := m.Search(ctx, "docs", "OrchardFault", knowledge.SearchOptions{Method: "search", MinScore: &high})
	if err != nil || len(r.Results) != 0 {
		t.Fatalf("score threshold: %+v %v", r, err)
	}
	for _, method := range []string{"vsearch", "gsearch"} {
		if _, err := m.Search(ctx, "docs", "OrchardFault", knowledge.SearchOptions{Method: method}); err == nil || !strings.Contains(err.Error(), "requires a complete") {
			t.Fatalf("unavailable %s did not fail explicitly: %v", method, err)
		}
	}
	if _, err := m.Search(ctx, "docs", "OrchardFault", knowledge.SearchOptions{Method: "search", Filter: "ext ="}); err == nil {
		t.Fatal("invalid filter was silently discarded")
	}
}

func TestLiveGraphSearchEvidenceAndHybridRecall(t *testing.T) {
	const body = "Payment Service depends on Order Database."
	const secondBody = "Order Database depends on Storage Service."
	m, l, ctx := liveSearchManager(t, map[string]string{"architecture.md": body, "storage.md": secondBody})
	t.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	t.Setenv("no_proxy", "127.0.0.1,localhost,::1")
	extraction := func(subject, object, subjectAlias, objectAlias, passage string) map[string]any {
		return map[string]any{
			"entities": []any{
				map[string]any{"local_id": "e1", "type": "system", "name": subject, "aliases": []string{subjectAlias}, "mentions": []string{subject}},
				map[string]any{"local_id": "e2", "type": "system", "name": object, "aliases": []string{objectAlias}, "mentions": []string{object}},
			},
			"relations": []any{map[string]any{"subject": "e1", "predicate": "depends_on", "object": "e2", "evidence": passage, "confidence": .92}},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected model operation", 400)
			return
		}
		var request struct {
			Messages []struct{ Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "invalid extraction request", 400)
			return
		}
		result := extraction("Payment Service", "Order Database", "支付服务", "订单数据库", body)
		if strings.Contains(request.Messages[len(request.Messages)-1].Content, secondBody) {
			result = extraction("Order Database", "Storage Service", "订单数据库", "存储服务", secondBody)
		}
		content, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	// Only this temporary fixture is enriched; Platform's worker continues to
	// maintain text/vectors and does not claim automatic graph construction.
	cfg, _ := json.Marshal(map[string]any{"models": map[string]any{"graph_extraction": map[string]any{"url": server.URL + "/v1/chat/completions", "model": "graph-fixture", "concurrency": 1, "timeout_ms": 5000}}})
	if out, err := m.runner.Run(ctx, l.database, cfg, "graph"); err != nil {
		t.Fatalf("fixture graph: %v %s", err, out)
	}
	o := knowledge.SearchOptions{Method: "gsearch", Entities: []string{"Payment Service"}, Relations: []string{"depends_on"}, Direction: "out", MaxHops: 3, Filter: "ext = md"}
	r, err := m.Search(ctx, "docs", "Payment Service", o)
	if err != nil || len(r.Results) == 0 || !slices.Contains(r.RetrievalChannels, "graph") {
		t.Fatalf("graph retrieval: %+v %v", r, err)
	}
	hit := r.Results[0]
	if hit.Graph == nil || hit.Graph.BestPath == nil || len(hit.Graph.BestPath.Edges) != 1 || hit.Graph.BestPath.Edges[0].Predicate != "depends_on" {
		t.Fatalf("relationship path lost: %+v", hit)
	}
	for _, edge := range hit.Graph.BestPath.Edges {
		for _, evidence := range edge.Evidence {
			read, err := m.Read("docs", knowledge.ReadOptions{ChunkID: evidence.EvidenceID})
			if err != nil || evidence.Path != "architecture.md" || read.Content != evidence.Content || !strings.Contains(read.Content, "depends on") {
				t.Fatalf("graph evidence readback: %+v %+v %v", evidence, read, err)
			}
		}
	}
	foundSecondHop := false
	for _, hit := range r.Results {
		if hit.Path == "storage.md" && hit.Graph != nil && hit.Graph.BestPath != nil && len(hit.Graph.BestPath.Edges) == 2 {
			foundSecondHop = true
		}
	}
	if !foundSecondHop {
		t.Fatalf("two-hop relationship was lost: %+v", r)
	}
	o.MaxHops = 1
	oneHop, err := m.Search(ctx, "docs", "Payment Service", o)
	if err != nil || len(oneHop.Results) != 1 || oneHop.Results[0].Path != "architecture.md" {
		t.Fatalf("maxHops was not enforced: %+v %v", oneHop, err)
	}
	o.MaxHops = 3
	o.Direction = "in"
	empty, err := m.Search(ctx, "docs", "Payment Service", o)
	if err != nil || len(empty.Results) != 0 {
		t.Fatalf("direction was not enforced: %+v %v", empty, err)
	}
	o.Direction, o.Filter = "out", `path ^= "missing"`
	empty, err = m.Search(ctx, "docs", "Payment Service", o)
	if err != nil || len(empty.Results) != 0 {
		t.Fatalf("graph filter was not enforced: %+v %v", empty, err)
	}
	hybrid, err := m.Search(ctx, "docs", "支付服务依赖什么", knowledge.SearchOptions{})
	if err != nil || len(hybrid.Results) == 0 || !slices.Contains(hybrid.RetrievalChannels, "graph") {
		t.Fatalf("hybrid graph recall: %+v %v", hybrid, err)
	}
	lexical, err := m.Search(ctx, "docs", "支付服务依赖什么", knowledge.SearchOptions{NoGraph: true})
	if err != nil || slices.Contains(lexical.RetrievalChannels, "graph") {
		t.Fatalf("noGraph failed: %+v %v", lexical, err)
	}
}
