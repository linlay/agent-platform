package kbases

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type schedulerEngine struct {
	mu      sync.Mutex
	calls   []map[string][]string
	entered chan struct{}
	release chan struct{}
	failure error
}

func (e *schedulerEngine) Update(ctx context.Context, db string, c []Collection) error {
	return e.UpdatePaths(ctx, db, c, nil)
}
func (e *schedulerEngine) UpdatePaths(ctx context.Context, db string, _ []Collection, changes map[string][]string) error {
	e.mu.Lock()
	e.calls = append(e.calls, changes)
	entered, release, failure := e.entered, e.release, e.failure
	e.entered = nil
	e.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := os.WriteFile(db, []byte("fixture"), 0600); err != nil {
		return err
	}
	return failure
}
func (*schedulerEngine) Read(context.Context, string, string, string, int, ...string) (json.RawMessage, error) {
	return json.RawMessage(`{"results":[]}`), nil
}
func newSchedulerService(t *testing.T, e Engine) *Service {
	t.Helper()
	s, err := New(context.Background(), t.TempDir(), t.TempDir(), e, Options{Debounce: 20 * time.Millisecond, ReconcileInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestLibraryRefreshRetainsReadsAndIsolatesOtherLibraries(t *testing.T) {
	e := &schedulerEngine{}
	s := newSchedulerService(t, e)
	d := createFixture(t, s)
	s.Refresh(d.ID)
	waitState(t, s, d.ID, "ready")
	e.mu.Lock()
	e.entered = make(chan struct{})
	e.release = make(chan struct{})
	entered, release := e.entered, e.release
	e.mu.Unlock()
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	<-entered
	defer close(release)
	got, err := s.Get(d.ID)
	if err != nil || !got.Stale || !got.Indexing || got.IndexedAt == 0 {
		t.Fatalf("lost readable generation: %+v %v", got, err)
	}
	if _, err = s.Read(context.Background(), d.ID, "files", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Edit(d.ID, Input{Name: "blocked"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("edit must fail promptly: %v", err)
	}
	other := createFixture(t, s)
	if _, err = s.Edit(other.ID, Input{Name: "independent"}); err != nil {
		t.Fatal(err)
	}
}
func TestEmbeddingFailureReadableUnknownFailureClosed(t *testing.T) {
	e := &schedulerEngine{failure: &ReadableFailure{Err: errors.New("embed failed")}}
	s := newSchedulerService(t, e)
	d := createFixture(t, s)
	s.Refresh(d.ID)
	waitState(t, s, d.ID, "ready")
	got, _ := s.Get(d.ID)
	if !got.Degraded || !got.Stale || got.IndexedAt == 0 {
		t.Fatalf("degraded result: %+v", got)
	}
	if _, _, release, err := s.Acquire(d.ID); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	e.mu.Lock()
	e.failure = errors.New("unknown partial update")
	e.mu.Unlock()
	s.refresh(d.ID, map[string][]string{"workspace": {"new.md"}})
	waitState(t, s, d.ID, "error")
	e.mu.Lock()
	lastChanges := e.calls[len(e.calls)-1]
	e.mu.Unlock()
	if lastChanges != nil {
		t.Fatal("degraded library must reconcile all collections")
	}

	if _, _, release, err := s.Acquire(d.ID); err == nil {
		release()
		t.Fatal("partial update allowed")
	}
}
func TestSchedulerWatchesChangesDuringUpdateAndReconcilesRestart(t *testing.T) {
	e := &schedulerEngine{entered: make(chan struct{}), release: make(chan struct{})}
	s := newSchedulerService(t, e)
	d := createFixture(t, s)
	entered, release := e.entered, e.release
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial update missing")
	}
	if err := os.WriteFile(filepath.Join(d.Collections[0].SourcePath, "during.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		e.mu.Lock()
		for _, batch := range e.calls {
			for _, paths := range batch {
				for _, p := range paths {
					found = found || p == "during.md"
				}
			}
		}
		e.mu.Unlock()
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("edit during maintenance lost")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), s.root, s.runtimeRoot, &schedulerEngine{}, Options{Debounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	if err = reopened.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	waitState(t, reopened, d.ID, "ready")
}
func TestHeldCreationAndReferencedDeletion(t *testing.T) {
	s := newSchedulerService(t, &schedulerEngine{})
	d, release, err := s.CreateHeld(Input{Name: "new", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Refresh(d.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("held library indexed", err)
	}
	if err = s.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	release()
	d = createFixture(t, s)
	s.options.References = func(string) []string { return []string{"general", "coder"} }
	var ref *ReferencedError
	if err = s.Delete(d.ID); !errors.As(err, &ref) || len(ref.Agents) != 2 {
		t.Fatal(err)
	}
}
func TestSchedulerIgnoresLegacyLayoutAndRebuildsCurrentLibrary(t *testing.T) {
	s := newSchedulerService(t, &schedulerEngine{})
	d := createFixture(t, s)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(s.runtimeRoot, "libraries")
	if err := os.Mkdir(legacyRoot, 0700); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(legacyRoot, d.ID)
	if err := os.Rename(filepath.Join(s.runtimeRoot, d.ID), legacyDir); err != nil {
		t.Fatal(err)
	}
	legacyFiles := map[string]string{
		"index.sqlite": "invalid legacy index",
		"state.json":   "invalid legacy state",
	}
	for name, content := range legacyFiles {
		if err := os.WriteFile(filepath.Join(legacyDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := New(context.Background(), s.root, s.runtimeRoot, &schedulerEngine{}, Options{Debounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	if err := reopened.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, reopened, d.ID, "ready")
	if data, err := os.ReadFile(filepath.Join(s.runtimeRoot, d.ID, "index.sqlite")); err != nil || string(data) != "fixture" {
		t.Fatalf("current index was not rebuilt: %q %v", data, err)
	}
	list, err := reopened.List()
	if err != nil || len(list) != 1 || list[0].ID != d.ID || list[0].Orphaned || list[0].InvalidID {
		t.Fatalf("legacy layout appeared in library list: %+v %v", list, err)
	}
	for name, content := range legacyFiles {
		if data, err := os.ReadFile(filepath.Join(legacyDir, name)); err != nil || string(data) != content {
			t.Fatalf("legacy %s changed: %q %v", name, data, err)
		}
	}
}

func TestReservedIDAndSourceScope(t *testing.T) {
	if ValidID("libraries") {
		t.Fatal("legacy layout name must remain a reserved ID")
	}
	s := newSchedulerService(t, &schedulerEngine{})
	for _, c := range []Collection{{Name: "docs", SourcePath: t.TempDir(), Include: []string{}}, {Name: "docs", SourcePath: t.TempDir(), Include: []string{"docs/*.md"}}} {
		if _, err := s.Create(Input{Name: "bad", Collections: []Collection{c}}); err == nil {
			t.Fatal("bad scope accepted")
		}
	}
}
