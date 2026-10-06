package kbx

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/kbase"
)

type testSource map[string]kbase.AgentSpec

func (s testSource) Agent(key string) (kbase.AgentSpec, bool) { a, ok := s[key]; return a, ok }
func (s testSource) Agents() []kbase.AgentSpec {
	out := []kbase.AgentSpec{}
	for _, a := range s {
		out = append(out, a)
	}
	return out
}

type runFunc func(context.Context, string, []byte, ...string) ([]byte, error)

func (f runFunc) Run(c context.Context, p string, b []byte, a ...string) ([]byte, error) {
	return f(c, p, b, a...)
}
func newTestManager(t *testing.T) (*Manager, library) {
	t.Helper()
	cfg := kbase.DefaultConfig()
	cfg.Enabled = true
	source := testSource{"docs": {Key: "docs", WorkspaceRoot: t.TempDir(), Config: cfg}}
	m := NewManager(Options{RuntimeDir: t.TempDir()}, source, nil)
	l, e := m.resolve("docs")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(l.database), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(l.database, nil, 0600); e != nil {
		t.Fatal(e)
	}
	return m, l
}
func responseJSON(value any) []byte {
	b, _ := json.Marshal(map[string]any{"schemaVersion": 2, "type": "kbx.agent.response", "status": "ok", "data": value})
	return b
}

const locator = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa#bytes=0-40"

func TestSearchDelegatesFiltersAndKeepsMultipleChunks(t *testing.T) {
	m, l := newTestManager(t)
	m.runner = runFunc(func(_ context.Context, db string, cfg []byte, args ...string) ([]byte, error) {
		if db != l.database {
			t.Fatal("wrong library")
		}
		joined := strings.Join(args, " ")
		for _, part := range []string{`"op":"pathPrefix"`, `"op":"pathGlob"`, `"op":"extension"`, `"value":"docs"`, `-- -not-a-flag`} {
			if !strings.Contains(joined, part) {
				t.Fatalf("missing %s: %s", part, joined)
			}
		}
		return responseJSON(map[string]any{"type": "kbx.search.response", "retrievalVersion": 6, "trace": map[string]any{"resultUnit": "chunk", "degraded": true, "candidateBudgetExhausted": true}, "results": []any{map[string]any{"file": "kbx://workspace/docs/a.md", "chunk": map[string]any{"id": locator}, "evidence": map[string]any{"id": locator, "text": "first"}}, map[string]any{"file": "kbx://workspace/docs/a.md", "chunk": map[string]any{"id": strings.Replace(locator, "0-40", "40-80", 1)}, "evidence": map[string]any{"id": locator, "text": "second"}}}}), nil
	})
	h := kbase.NewToolHandler(m)
	r, e := h.Invoke(context.Background(), kbase.ToolSearch, map[string]any{"query": "-not-a-flag", "pathPrefix": "docs", "pathGlob": "**/*.md", "type": "MD"}, &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", KBaseEnabled: true}})
	if e != nil || r.Error != "" {
		t.Fatalf("%v %+v", e, r)
	}
	if r.Structured["count"] != 2 || r.Structured["degraded"] != true {
		t.Fatalf("%+v", r.Structured)
	}
	if _, ok := r.Structured["matchCount"]; ok {
		t.Fatal("invented exact count")
	}
	if len(r.SourcePublication.Sources) != 1 || len(r.SourcePublication.Sources[0].Chunks) != 2 {
		t.Fatal("lost chunks during source publication")
	}
}
func TestRejectSearchOffsetBeforeCLI(t *testing.T) {
	m, _ := newTestManager(t)
	m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
		t.Fatal("CLI must not run")
		return nil, nil
	})
	if _, e := m.Search(context.Background(), "docs", "x", kbase.SearchOptions{Offset: 1}); e == nil {
		t.Fatal("offset accepted")
	}
}
func TestReadEvidenceAndRejectForeignCollection(t *testing.T) {
	m, _ := newTestManager(t)
	m.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "--evidence "+locator) {
			t.Fatal(args)
		}
		return responseJSON(map[string]any{"file": "kbx://other/secret.md", "evidence": map[string]any{"text": "secret"}}), nil
	})
	if _, e := m.Read("docs", kbase.ReadOptions{ChunkID: locator}); e == nil {
		t.Fatal("foreign collection accepted")
	}
}
func TestScopeChangeGetsDifferentIndexAndDisabledAgentFails(t *testing.T) {
	m, l := newTestManager(t)
	s := m.agents.(testSource)
	a := s["docs"]
	a.WorkspaceRoot = t.TempDir()
	s["docs"] = a
	next, e := m.resolve("docs")
	if e != nil || next.database == l.database {
		t.Fatalf("scope not isolated: %v", e)
	}
	a.Config.Enabled = false
	s["docs"] = a
	if e = m.ValidateAgent("docs"); kbase.KindOf(e) != kbase.ErrorNotFound {
		t.Fatalf("disabled capability exposed: %v", e)
	}
}
func TestRefreshRequiresStartedScheduler(t *testing.T) {
	m, _ := newTestManager(t)
	m.runner = runFunc(func(context.Context, string, []byte, ...string) ([]byte, error) {
		t.Fatal("unstarted scheduler must not invoke CLI")
		return nil, nil
	})
	if _, e := m.Refresh(context.Background(), "docs", kbase.RefreshOptions{}); kbase.KindOf(e) != kbase.ErrorUnavailable {
		t.Fatalf("%v", e)
	}
}

func TestIndexedFilesUnicodeGlobAndUnknownCounts(t *testing.T) {
	files, e := kbase.FormatIndexedFiles([]kbase.FileEntry{{Path: "规章/风险.md", Ext: ".md", Status: "active"}}, kbase.FilesOptions{Pattern: "规章/*.md", HeadLimit: 10})
	if e != nil || len(files.Results) != 1 {
		t.Fatalf("Unicode glob: %v %+v", e, files)
	}
	b, e := json.Marshal(kbase.Status{Engine: "kbx"})
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	if e = json.Unmarshal(b, &fields); e != nil {
		t.Fatal(e)
	}
	if _, ok := fields["chunks"]; ok {
		t.Fatal("unknown count represented as zero")
	}
	if fields["chunksKnown"] != false {
		t.Fatal("unknown count not explicit")
	}
}
