package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestWatcherRecursesAndWatchesCreatedDirs(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing: %v", err)
	}
	changes := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher, err := Start(ctx, Spec{
		LogPrefix: "[watch-test]",
		Roots:     []Root{{Path: root, Label: "root", Recursive: true}},
		Debounce:  20 * time.Millisecond,
		OnEvent: func(event Event) {
			changes <- filepath.Base(event.Path)
		},
		OnDebounce: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer waitDone(t, cancel, watcher)
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(existing, "one.yml"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write existing child: %v", err)
	}
	assertSawChange(t, changes, "one.yml")

	created := filepath.Join(root, "created")
	if err := os.MkdirAll(created, 0o755); err != nil {
		t.Fatalf("mkdir created: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(created, "two.yml"), []byte("two"), 0o644); err != nil {
		t.Fatalf("write created child: %v", err)
	}
	assertSawChange(t, changes, "two.yml")
}

func TestWatcherDebouncesAndFilters(t *testing.T) {
	root := t.TempDir()
	debounced := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watcher, err := Start(ctx, Spec{
		LogPrefix: "[watch-test]",
		Roots:     []Root{{Path: root, Label: "root", Recursive: true}},
		Debounce:  50 * time.Millisecond,
		Ignore: func(path string) bool {
			return strings.HasSuffix(path, ".DS_Store")
		},
		Include: func(event Event) bool {
			return strings.HasSuffix(event.Path, ".yml")
		},
		OnDebounce: func(context.Context) error {
			debounced <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer waitDone(t, cancel, watcher)
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write ignored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write filtered: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.yml"), []byte("one"), 0o644); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.yml"), []byte("two"), 0o644); err != nil {
		t.Fatalf("write two: %v", err)
	}

	select {
	case <-debounced:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounce")
	}
	select {
	case <-debounced:
		t.Fatal("expected yml writes in one debounce window to trigger once")
	case <-time.After(150 * time.Millisecond):
	}
}

func assertSawChange(t *testing.T, changes <-chan string, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-changes:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for change %q", want)
		}
	}
}

func waitDone(t *testing.T, cancel context.CancelFunc, watcher *Watcher) {
	t.Helper()
	cancel()
	select {
	case <-watcher.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher stop")
	}
}

func TestPruneReleasesBackendWatches(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "skill", "scripts")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer fsw.Close()
	w := &Watcher{fsw: fsw, watched: map[string]struct{}{}}
	if err := w.addRoot(Root{Path: root, Recursive: true}); err != nil {
		t.Fatal(err)
	}
	w.prune(filepath.Join(root, "skill"))
	if got := fsw.WatchList(); len(got) != 1 || got[0] != root {
		t.Fatalf("backend watches after prune: %v", got)
	}
	if w.Watched() != 1 {
		t.Fatalf("watch bookkeeping after prune: %d", w.Watched())
	}
}

func TestDebounceDoesNotOverlap(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(release)
	w, err := Start(ctx, Spec{
		Roots:    []Root{{Path: root}},
		Debounce: 5 * time.Millisecond,
		OnDebounce: func(context.Context) error {
			started <- struct{}{}
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not start")
	}
	write("second")
	select {
	case <-started:
		t.Fatal("overlapping callback")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	release <- struct{}{}
	select {
	case <-w.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestSuspendReleasesHandlesAndResumeWatchesNewDirectories(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "skill", "scripts")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	changes := make(chan string, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := Start(ctx, Spec{
		Roots: []Root{{Path: root, Recursive: true}},
		OnEvent: func(e Event) {
			select {
			case changes <- filepath.Base(e.Path):
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer waitDone(t, cancel, w)
	previousBackend := w.fsw
	resume, err := w.Suspend()
	if err != nil {
		t.Fatal(err)
	}
	if err := previousBackend.Add(root); !errors.Is(err, fsnotify.ErrClosed) {
		t.Fatalf("old backend was not closed: %v", err)
	}
	if n := len(w.fsw.WatchList()); n != 0 {
		t.Fatalf("%d backend handles during suspension", n)
	}
	if w.Watched() != 0 {
		t.Fatalf("bookkeeping still contains watches")
	}
	if err := os.Rename(filepath.Join(root, "skill"), filepath.Join(root, "backup")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := resume(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "after.yml"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertSawChange(t, changes, "after.yml")
}

func TestResumeSchedulesReconciliationWithoutNewEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resumed := make(chan struct{}, 1)
	reloaded := make(chan struct{}, 1)
	w, err := Start(ctx, Spec{
		Roots:    []Root{{Path: t.TempDir()}},
		Debounce: time.Millisecond,
		OnResume: func() { resumed <- struct{}{} },
		OnDebounce: func(context.Context) error {
			select {
			case <-resumed:
			default:
				t.Error("reload before resume hook")
			}
			reloaded <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer waitDone(t, cancel, w)
	resume, err := w.Suspend()
	if err != nil {
		t.Fatal(err)
	}
	if err := resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("resume did not reconcile")
	}
}
