package kbx

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
	"time"
)

type testSource map[string]knowledge.AgentSpec

func (s testSource) Agent(key string) (knowledge.AgentSpec, bool) { a, ok := s[key]; return a, ok }
func (s testSource) Agents() []knowledge.AgentSpec {
	out := []knowledge.AgentSpec{}
	for _, a := range s {
		out = append(out, a)
	}
	return out
}

type runFunc func(context.Context, string, []byte, ...string) ([]byte, error)

func (f runFunc) Run(c context.Context, p string, b []byte, a ...string) ([]byte, error) {
	return f(c, p, b, a...)
}

type readyEngine struct{}

func (readyEngine) Update(_ context.Context, db string, _ []kbases.Collection) error {
	return os.WriteFile(db, nil, 0600)
}
func (readyEngine) Read(context.Context, string, string, string, int, ...string) (json.RawMessage, error) {
	return nil, nil
}
func newTestManager(t *testing.T) (*Manager, library) {
	t.Helper()
	root, runtime := t.TempDir(), t.TempDir()
	libraryService, err := kbases.New(context.Background(), t.TempDir(), runtime, readyEngine{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { libraryService.Close(context.Background()) })
	d, err := libraryService.Create(kbases.Input{Name: "fixture", SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = libraryService.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		d, err = libraryService.Get(d.ID)
		if err == nil && d.State == "ready" {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}
	cfg := knowledge.DefaultConfig()
	cfg.Enabled = true
	cfg.LibraryID = d.ID
	m := NewManager(Options{KBases: libraryService}, testSource{"docs": {Key: "docs", WorkspaceRoot: d.Collections[0].SourcePath, Config: cfg}}, nil)
	l, err := m.resolve("docs")
	if err != nil {
		t.Fatal(err)
	}
	l.release()
	l.release = nil
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
	h := knowledge.NewToolHandler(m)
	r, e := h.Invoke(context.Background(), knowledge.ToolSearch, map[string]any{"query": "-not-a-flag", "pathPrefix": "workspace/docs", "pathGlob": "**/*.md", "type": "MD"}, &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", KBaseEnabled: true}})
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
	if _, e := m.Search(context.Background(), "docs", "x", knowledge.SearchOptions{Offset: 1}); e == nil {
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
	if _, e := m.Read("docs", knowledge.ReadOptions{ChunkID: locator}); e == nil {
		t.Fatal("foreign collection accepted")
	}
}
func TestWorkspaceChangeReusesLibraryAndUnboundAgentFails(t *testing.T) {
	m, l := newTestManager(t)
	source := m.agents.(testSource)
	a := source["docs"]
	a.WorkspaceRoot = t.TempDir()
	source["docs"] = a
	next, err := m.resolve("docs")
	if err != nil {
		t.Fatal(err)
	}
	next.release()
	if next.database != l.database {
		t.Fatal("Workspace changed library storage")
	}
	a.Config.LibraryID = ""
	source["docs"] = a
	if knowledge.KindOf(m.ValidateAgent("docs")) != knowledge.ErrorNotFound {
		t.Fatal("unbound agent accepted")
	}
}

func TestIndexedFilesUnicodeGlobAndUnknownCounts(t *testing.T) {
	files, e := knowledge.FormatIndexedFiles([]knowledge.FileEntry{{Path: "规章/风险.md", Ext: ".md", Status: "active"}}, knowledge.FilesOptions{Pattern: "规章/*.md", HeadLimit: 10})
	if e != nil || len(files.Results) != 1 {
		t.Fatalf("Unicode glob: %v %+v", e, files)
	}
	b, e := json.Marshal(knowledge.Status{Engine: "kbx"})
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
