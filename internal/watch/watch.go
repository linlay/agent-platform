package watch

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var ErrNoWatchedRoots = errors.New("no watched roots")

type Root struct {
	Path           string
	Label          string
	Recursive      bool
	ShouldTraverse func(path string) bool
}

type Event struct {
	Path string
	Op   fsnotify.Op
}

type Spec struct {
	LogPrefix  string
	Roots      []Root
	Debounce   time.Duration
	Ignore     func(path string) bool
	Include    func(event Event) bool
	OnEvent    func(event Event)
	OnResume   func()
	OnDebounce func(ctx context.Context) error
	OnError    func(error)
}

type Watcher struct {
	fsw        *fsnotify.Watcher
	watched    map[string]struct{}
	mu         sync.Mutex
	done       chan struct{}
	commands   chan watchCommand
	callbackMu sync.Mutex
	pending    []fsnotify.Event
}

type watchCommand struct {
	resume bool
	result chan error
}

func Start(ctx context.Context, spec Spec) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{fsw: fsw, watched: map[string]struct{}{}, done: make(chan struct{}), commands: make(chan watchCommand)}
	for _, root := range spec.Roots {
		if err := w.drainSubscriptions(func() error { return w.addRoot(root) }, true); err != nil {
			log.Printf("%s skip watch %s (%s): %v", spec.prefix(), root.Path, root.Label, err)
			continue
		}
		log.Printf("%s watching: %s (%s)", spec.prefix(), root.Path, root.Label)
	}
	if w.Watched() == 0 {
		_ = fsw.Close()
		return nil, ErrNoWatchedRoots
	}

	go w.run(ctx, spec)
	return w, nil
}

// Suspend releases directory handles before a resource transaction. The caller
// must serialize transactions and coordinate reload callbacks separately.
func (w *Watcher) Suspend() (func() error, error) {
	resume := func() error { return w.command(true) }
	if err := w.command(false); err != nil {
		return nil, errors.Join(err, resume())
	}
	return resume, nil
}

func (w *Watcher) command(resume bool) error {
	cmd := watchCommand{resume: resume, result: make(chan error, 1)}
	select {
	case <-w.done:
		return errors.New("watcher stopped")
	case w.commands <- cmd:
	}
	select {
	case err := <-cmd.result:
		return err
	case <-w.done:
		return errors.New("watcher stopped")
	}
}

// Windows fsnotify subscription operations wait for its event-delivery goroutine. Keep
// draining while changing subscriptions so a full event stream cannot deadlock
// suspension. Mutations explicitly reload their catalog after this interval.
func (w *Watcher) drainDuring(fn func() error) error {
	return w.drainSubscriptions(fn, false)
}

func (w *Watcher) drainSubscriptions(fn func() error, retain bool) error {
	done := make(chan error, 1)
	// Capture channels before fn may replace the backend during suspension.
	events, errs := w.fsw.Events, w.fsw.Errors
	go func() { done <- fn() }()
	for {
		select {
		case err := <-done:
			return err
		case event, ok := <-events:
			if !ok {
				events = nil
			} else if retain {
				w.pending = append(w.pending, event)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
			} else {
				log.Printf("[watch] subscription change error: %v", err)
			}
		}
	}
}

func (w *Watcher) removeAll() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Windows Remove resolves the current path before releasing a watch. An
	// externally renamed directory can therefore leave an orphaned handle
	// that Remove(oldPath) cannot reach. Close waits for the backend to release
	// every handle, including those no longer represented by a live pathname.
	if err := w.fsw.Close(); err != nil {
		return err
	}
	w.watched = map[string]struct{}{}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fsw = fsw
	return nil
}

func (s Spec) prefix() string {
	if strings.TrimSpace(s.LogPrefix) == "" {
		return "[watch]"
	}
	return strings.TrimSpace(s.LogPrefix)
}

func (w *Watcher) Watched() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watched)
}

func (w *Watcher) Done() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.done
}

func (w *Watcher) addRoot(root Root) error {
	root.Path = filepath.Clean(root.Path)
	if !root.Recursive {
		return w.addDir(root.Path)
	}
	return filepath.WalkDir(root.Path, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if path != root.Path && root.ShouldTraverse != nil && !root.ShouldTraverse(path) {
			return filepath.SkipDir
		}
		return w.addDir(path)
	})
}

func (w *Watcher) addCreatedDir(root Root, path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil
	}
	path = filepath.Clean(path)
	root.Path = filepath.Clean(root.Path)
	if !insideDir(root.Path, path) {
		return nil
	}
	if root.ShouldTraverse != nil && !root.ShouldTraverse(path) {
		return nil
	}
	if !root.Recursive {
		return w.addDir(path)
	}
	return filepath.WalkDir(path, func(child string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		if child != path && root.ShouldTraverse != nil && !root.ShouldTraverse(child) {
			return filepath.SkipDir
		}
		return w.addDir(child)
	})
}

func (w *Watcher) addDir(path string) error {
	path = filepath.Clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.watched[path]; ok {
		return nil
	}
	if err := w.fsw.Add(path); err != nil {
		return err
	}
	w.watched[path] = struct{}{}
	return nil
}

func (w *Watcher) prune(path string) {
	path = filepath.Clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	for dir := range w.watched {
		if dir == path || strings.HasPrefix(dir, path+string(os.PathSeparator)) {
			// Some backends remove deleted watches automatically, but renamed
			// descendants may remain registered and must be explicitly released.
			_ = w.fsw.Remove(dir)
			delete(w.watched, dir)
		}
	}
}

func (w *Watcher) run(ctx context.Context, spec Spec) {
	defer func() {
		_ = w.fsw.Close()
		log.Printf("%s file watcher stopped", spec.prefix())
		close(w.done)
	}()
	suspended := false
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		events := w.fsw.Events
		queued := len(w.pending) > 0
		if queued {
			next := make(chan fsnotify.Event, 1)
			next <- w.pending[0]
			events = next
		}
		select {
		case <-ctx.Done():
			return
		case <-timerC:
			timerC = nil
			if ctx.Err() != nil {
				return
			}
			go func() {
				w.callbackMu.Lock()
				defer w.callbackMu.Unlock()
				if ctx.Err() != nil {
					return
				}
				if err := spec.OnDebounce(ctx); err != nil {
					log.Printf("%s reload failed: %v", spec.prefix(), err)
				}
			}()
		case cmd := <-w.commands:
			if !cmd.resume {
				if timer != nil {
					timer.Stop()
				}
				timerC = nil
				suspended = true
				w.pending = nil
				cmd.result <- w.drainDuring(w.removeAll)
			} else {
				restoreErr := w.drainDuring(func() error {
					var result error
					for _, root := range spec.Roots {
						if err := w.addRoot(root); err != nil && !os.IsNotExist(err) {
							result = errors.Join(result, err)
						}
					}
					return result
				})
				suspended = false
				if spec.OnResume != nil {
					spec.OnResume()
				}
				if spec.OnDebounce != nil {
					delay := spec.Debounce
					if delay <= 0 {
						delay = time.Millisecond
					}
					timer = time.NewTimer(delay)
					timerC = timer.C
				}
				cmd.result <- restoreErr
			}
		case event, ok := <-events:
			if queued {
				w.pending = w.pending[1:]
			}
			if !ok {
				return
			}
			if suspended {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			changed := filepath.Clean(event.Name)
			if spec.Ignore != nil && spec.Ignore(changed) {
				continue
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				_ = w.drainSubscriptions(func() error { w.prune(changed); return nil }, true)
			}
			for _, root := range spec.Roots {
				if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
					if err := w.drainSubscriptions(func() error { return w.addCreatedDir(root, changed) }, true); err != nil {
						log.Printf("%s watcher register failed for %s: %v", spec.prefix(), changed, err)
					}
				}
			}
			watchEvent := Event{Path: changed, Op: event.Op}
			if spec.Include != nil && !spec.Include(watchEvent) {
				continue
			}
			if spec.OnEvent != nil {
				spec.OnEvent(watchEvent)
			}
			if spec.OnDebounce == nil {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			delay := spec.Debounce
			if delay <= 0 {
				delay = time.Millisecond
			}
			timer = time.NewTimer(delay)
			timerC = timer.C
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("%s watcher error: %v", spec.prefix(), err)
			if spec.OnError != nil {
				spec.OnError(err)
			}
		}
	}
}

func insideDir(root string, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}
