package kbasescenter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testEngine struct {
	release chan struct{}
	err     error
}

type recordingEngine struct {
	testEngine
	operation string
	selected  []string
}

func (e *recordingEngine) Read(_ context.Context, _, operation, _ string, _ int, selected ...string) (json.RawMessage, error) {
	e.operation = operation
	e.selected = append([]string(nil), selected...)
	return json.RawMessage(`{"results":[]}`), nil
}

func TestMultipleCollectionsAndSearchScope(t *testing.T) {
	engine := &recordingEngine{}
	s, err := New(context.Background(), t.TempDir(), t.TempDir(), engine)
	if err != nil {
		t.Fatal(err)
	}
	collections := []Collection{{Name: "docs", SourcePath: t.TempDir()}, {Name: "报告", SourcePath: t.TempDir()}}
	d, err := s.Create(Input{Name: "Combined", Collections: collections})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Collections) != 2 || d.SourcePath != "" {
		t.Fatalf("definition: %+v", d)
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	if _, err = s.Search(context.Background(), d.ID, SearchInput{Query: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if engine.operation != "query" || len(engine.selected) != 2 {
		t.Fatalf("default scope: %+v", engine)
	}
	for _, method := range []string{"query", "search", "vsearch", "gsearch"} {
		if _, err = s.Search(context.Background(), d.ID, SearchInput{Query: "fixture", Method: method, Collections: []string{"报告"}}); err != nil {
			t.Fatal(err)
		}
		if engine.operation != method || len(engine.selected) != 1 || engine.selected[0] != "报告" {
			t.Fatalf("selected scope: %+v", engine)
		}
	}
	for _, input := range []SearchInput{{Query: "fixture", Method: "get"}, {Query: "fixture", Collections: []string{"foreign"}}} {
		if _, err = s.Search(context.Background(), d.ID, input); err == nil {
			t.Fatal("invalid search accepted")
		}
	}
	if _, err = s.Read(context.Background(), d.ID, "read", "kbx://报告/2026/a..b.md", 0); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"kbx://foreign/a.md", "kbx://docs/../a.md", "kbx://docs/a/../../b.md", "kbx://docs/\\secret"} {
		if _, err = s.Read(context.Background(), d.ID, "read", ref, 0); err == nil {
			t.Fatalf("invalid reference accepted: %s", ref)
		}
	}
	if _, err = s.Edit(d.ID, Input{Name: "Renamed", Collections: d.Collections}); err != nil {
		t.Fatal(err)
	}
	if edited, err := s.Edit(d.ID, Input{Name: "Changed", Collections: []Collection{d.Collections[0]}}); err != nil || len(edited.Collections) != 1 || edited.IndexedAt != 0 || edited.State != "unindexed" {
		t.Fatalf("collection removal: %+v %v", edited, err)
	}
	reopened, _ := New(context.Background(), s.root, s.runtimeRoot, engine)
	persisted, err := reopened.Get(d.ID)
	if err != nil || len(persisted.Collections) != 1 {
		t.Fatalf("persistence: %+v %v", persisted, err)
	}
}

func TestCollectionValidation(t *testing.T) {
	s, _ := New(context.Background(), t.TempDir(), t.TempDir(), testEngine{})
	path := t.TempDir()
	for _, collections := range [][]Collection{
		{}, {{Name: "docs", SourcePath: path}, {Name: "docs", SourcePath: t.TempDir()}},
		{{Name: "docs", SourcePath: path}, {Name: "reports", SourcePath: path}},
		{{Name: "a/b", SourcePath: path}}, {{Name: "valid", SourcePath: "relative"}},
		{{Name: "valid", SourcePath: s.root}},
	} {
		if _, err := s.Create(Input{Name: "Invalid", Collections: collections}); err == nil {
			t.Fatalf("invalid collections accepted: %+v", collections)
		}
	}
	if _, err := s.Create(Input{Name: "Ambiguous", SourcePath: path, Collections: []Collection{{Name: "docs", SourcePath: path}}}); err == nil {
		t.Fatal("ambiguous sources accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Input{Name: "Alias", Collections: []Collection{{Name: "docs", SourcePath: path}, {Name: "reports", SourcePath: alias}}}); err == nil {
		t.Fatal("duplicate canonical sources accepted")
	}
	d, err := s.Create(Input{Name: "Existing", SourcePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(d.ID); err != nil {
		t.Fatal(err)
	}
}

func TestEditCollectionScope(t *testing.T) {
	s, _ := New(context.Background(), t.TempDir(), t.TempDir(), testEngine{})
	d, err := s.Create(Input{Name: "Docs", Collections: []Collection{{Name: "docs", SourcePath: t.TempDir()}, {Name: "reports", SourcePath: t.TempDir()}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	d = waitState(t, s, d.ID, "ready")
	indexedAt := d.IndexedAt
	// Reordering and display-only edits preserve the successful index.
	reordered := []Collection{d.Collections[1], d.Collections[0]}
	d, err = s.Edit(d.ID, Input{Name: "Renamed", Collections: reordered})
	if err != nil || d.State != "ready" || d.IndexedAt != indexedAt {
		t.Fatalf("metadata edit invalidated index: %+v %v", d, err)
	}
	original := append([]Collection(nil), d.Collections...)
	added := Collection{Name: "notes", SourcePath: t.TempDir()}
	d, err = s.Edit(d.ID, Input{Name: "Renamed", Collections: append(reordered, added)})
	if err != nil || len(d.Collections) != 3 || d.State != "unindexed" || d.IndexedAt != 0 {
		t.Fatalf("add: %+v %v", d, err)
	}
	if _, err = s.Read(context.Background(), d.ID, "files", "", 0); err == nil {
		t.Fatal("stale index exposed after editing sources")
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	d = waitState(t, s, d.ID, "ready")
	rebound := Collection{Name: "docs", SourcePath: t.TempDir()}
	d, err = s.Edit(d.ID, Input{Name: "Renamed", Collections: []Collection{rebound, added}})
	if err != nil || len(d.Collections) != 2 || d.State != "unindexed" || d.IndexedAt != 0 {
		t.Fatalf("remove/rebind: %+v %v", d, err)
	}
	for _, c := range original {
		if _, err := os.Stat(c.SourcePath); err != nil {
			t.Fatalf("source removed: %v", err)
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(d.Collections[0].SourcePath, alias); err != nil {
		t.Fatal(err)
	}
	invalid := [][]Collection{
		{}, {{Name: "docs", SourcePath: rebound.SourcePath}, {Name: "docs", SourcePath: added.SourcePath}},
		{{Name: "docs", SourcePath: rebound.SourcePath}, {Name: "alias", SourcePath: alias}},
		{{Name: "notes", SourcePath: s.root}}, {{Name: "notes", SourcePath: filepath.Join(t.TempDir(), "missing")}},
	}
	for _, collections := range invalid {
		if _, err := s.Edit(d.ID, Input{Name: "Invalid", Collections: collections}); err == nil {
			t.Fatalf("invalid edit accepted: %+v", collections)
		}
	}
	persisted, err := s.Get(d.ID)
	if err != nil || persisted.Name != "Renamed" || len(persisted.Collections) != 2 {
		t.Fatalf("invalid edit changed definition: %+v %v", persisted, err)
	}
	// Display edits remain possible if an existing source goes offline.
	if err := os.Rename(persisted.Collections[0].SourcePath, persisted.Collections[0].SourcePath+"-offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit(d.ID, Input{Name: "Offline source", Collections: persisted.Collections}); err != nil {
		t.Fatal(err)
	}
}

func (e testEngine) Update(ctx context.Context, db string, collections []Collection) error {
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if e.err != nil {
		return e.err
	}
	return os.WriteFile(db, []byte("index"), 0600)
}
func (e testEngine) Read(context.Context, string, string, string, int, ...string) (json.RawMessage, error) {
	return json.RawMessage(`{"results":[]}`), nil
}
func waitState(t *testing.T, s *Service, id, want string) Definition {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if d.State == want {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("state did not reach " + want)
	return Definition{}
}
func TestLibraryLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kbases")
	source := t.TempDir()
	release := make(chan struct{})
	s, err := New(context.Background(), root, root+"-runtime", testEngine{release: release})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Create(Input{Name: "Docs", SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(context.Background(), d.ID, "search", "x", 5); err == nil {
		t.Fatal("unindexed search accepted")
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(d.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("deleted indexing library", err)
	}
	if _, err = s.Edit(d.ID, Input{Name: "Busy", Collections: []Collection{{Name: "new", SourcePath: source}}}); !errors.Is(err, ErrBusy) {
		t.Fatal("edited indexing library", err)
	}
	if _, err = s.Refresh(d.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("duplicate refresh accepted", err)
	}
	close(release)
	waitState(t, s, d.ID, "ready")
	if _, err = s.Edit(d.ID, Input{Name: "Renamed", Description: "Notes"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), root, root+"-runtime", testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := reopened.List()
	if err != nil || len(items) != 1 || items[0].Name != "Renamed" {
		t.Fatalf("persistence: %+v %v", items, err)
	}
	if _, err = s.Read(context.Background(), d.ID, "search", "x", 51); err == nil {
		t.Fatal("invalid limit accepted")
	}
	if _, err = s.Read(context.Background(), d.ID, "read", "kbx://other/secret", 0); err == nil {
		t.Fatal("foreign collection accepted")
	}
	if err = s.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatal("source removed")
	}
	if _, err = s.Get(d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestLibraryValidationAndRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kbases")
	s, _ := New(context.Background(), root, root+"-runtime", testEngine{err: errors.New("failed")})
	for _, source := range []string{"relative", root, filepath.Dir(root)} {
		if _, err := s.Create(Input{Name: "bad", SourcePath: source}); err == nil {
			t.Fatal("unsafe source accepted", source)
		}
	}
	if _, err := s.Get("../escape"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	d, err := s.Create(Input{Name: "Valid", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "error")
	d.State = "indexing"
	if err = s.saveState(d.ID, runtimeState{State: d.State}); err != nil {
		t.Fatal(err)
	}
	recovered, _ := s.Get(d.ID)
	if recovered.State != "error" {
		t.Fatal("interrupted build reported running")
	}
	if err = os.RemoveAll(filepath.Join(root, d.ID)); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, d.ID)); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(d.ID); err == nil {
		t.Fatal("followed library symlink")
	}
}
