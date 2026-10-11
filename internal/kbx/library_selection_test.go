package kbx

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
)

func TestExplicitLibrariesAndPublishedSourceSurviveAgentUnbinding(t *testing.T) {
	m, first := newTestManager(t)
	second, err := m.options.KBases.Create(kbases.Input{Name: "Second", SourcePath: first.spec.WorkspaceRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.options.KBases.Refresh(second.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		second, err = m.options.KBases.Get(second.ID)
		if err == nil && second.State == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if second.State != "ready" {
		t.Fatal("second library not ready")
	}
	os.WriteFile(filepath.Join(first.spec.WorkspaceRoot, "note.md"), []byte("same document"), 0600)
	databases := map[string]bool{}
	m.runner = runFunc(func(_ context.Context, db string, _ []byte, args ...string) ([]byte, error) {
		databases[db] = true
		if args[0] == "get" {
			return responseJSON(map[string]any{"file": "kbx://workspace/note.md", "evidence": map[string]any{"id": locator, "text": "same document"}}), nil
		}
		return responseJSON(map[string]any{"type": "kbx.search.response", "retrievalVersion": 6, "trace": map[string]any{"resultUnit": "chunk"}, "results": []any{map[string]any{"file": "kbx://workspace/note.md", "chunk": map[string]any{"id": locator}, "evidence": map[string]any{"id": locator, "text": "same document"}}}}), nil
	})
	m.agents = nil // No Agent binding is consulted by retrieval or published-source reads.
	for _, id := range []string{first.definition.ID, second.ID} {
		result, err := m.Search(context.Background(), id, "same", knowledge.SearchOptions{Method: "search"})
		if err != nil || result.LibraryID != id || len(result.Results) != 1 || result.Results[0].LibraryID != id {
			t.Fatalf("search %s: %+v %v", id, result, err)
		}
		read, err := m.Read(id, knowledge.ReadOptions{ChunkID: locator})
		if err != nil || read.LibraryID != id {
			t.Fatalf("same chunk identity resolved wrong library: %+v %v", read, err)
		}
		published, err := m.ReadBound("deleted-agent", id, knowledge.ReadOptions{ChunkID: locator})
		if err != nil || published.LibraryID != id {
			t.Fatalf("published source depends on binding: %+v %v", published, err)
		}
		file, err := m.OpenBoundSource("deleted-agent", id, "workspace/note.md")
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if len(databases) != 2 {
		t.Fatalf("libraries shared a database: %+v", databases)
	}
	if _, err := m.Search(context.Background(), "docs", "same", knowledge.SearchOptions{Method: "search"}); err == nil {
		t.Fatal("agent key silently fell back to a bound library")
	}
}

func TestFrozenAgentRetrievalOnlyAppliesToBoundLibrary(t *testing.T) {
	m, l := newTestManager(t)
	topK := 11
	d, err := m.options.KBases.Edit(l.definition.ID, kbases.Input{BaseRevision: l.definition.BaseRevision, Name: l.definition.Name, Retrieval: &knowledge.RetrievalSettings{TopK: &topK}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := knowledge.DefaultConfig()
	cfg.LibraryID = d.ID
	override := 3
	cfg.RetrievalOverrides = &knowledge.RetrievalSettings{TopK: &override}
	limit := 0
	m.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "-n" {
				limit, _ = strconv.Atoi(args[i+1])
			}
		}
		return responseJSON(map[string]any{"type": "kbx.search.response", "retrievalVersion": 6, "trace": map[string]any{"resultUnit": "chunk"}, "results": []any{}}), nil
	})
	for _, tc := range []struct {
		bound string
		call  int
		want  int
	}{{d.ID, 0, 3}, {"other", 0, 11}, {d.ID, 7, 7}} {
		cfg.LibraryID = tc.bound
		result, err := m.Search(context.Background(), d.ID, "x", knowledge.SearchOptions{Method: "search", AgentConfig: &cfg, Limit: tc.call})
		if err != nil || result.Limit != tc.want || limit != tc.want {
			t.Fatalf("override binding=%s call=%d: limit=%d result=%+v err=%v", tc.bound, tc.call, limit, result, err)
		}
	}
	if !strings.Contains(d.ID, l.definition.ID) {
		t.Fatal("retrieval-only update unexpectedly changed ID")
	}
}
