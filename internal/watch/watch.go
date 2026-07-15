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
	OnDebounce func(ctx context.Context) error
	OnError    func(error)
}

type Watcher struct {
	fsw     *fsnotify.Watcher
	watched map[string]struct{}
	mu      sync.Mutex
	done    chan struct{}
}

func Start(ctx context.Context, spec Spec) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{fsw: fsw, watched: map[string]struct{}{}, done: make(chan struct{})}
	go w.run(ctx, spec)
	for _, root := range spec.Roots {
		if err := w.addRoot(root); err != nil {
			log.Printf("%s skip watch %s (%s): %v", spec.prefix(), root.Path, root.Label, err)
			continue
		}
		log.Printf("%s watching: %s (%s)", spec.prefix(), root.Path, root.Label)
	}
	if w.Watched() == 0 {
		_ = fsw.Close()
		<-w.done
		return nil, ErrNoWatchedRoots
	}

	return w, nil
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
	if _, ok := w.watched[path]; ok {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()
	if err := w.fsw.Add(path); err != nil {
		return err
	}
	w.mu.Lock()
	w.watched[path] = struct{}{}
	w.mu.Unlock()
	return nil
}

func (w *Watcher) prune(path string) {
	path = filepath.Clean(path)
	w.mu.Lock()
	defer w.mu.Unlock()
	for dir := range w.watched {
		if dir == path || strings.HasPrefix(dir, path+string(os.PathSeparator)) {
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
	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			changed := filepath.Clean(event.Name)
			if spec.Ignore != nil && spec.Ignore(changed) {
				continue
			}
			for _, root := range spec.Roots {
				if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
					if err := w.addCreatedDir(root, changed); err != nil {
						log.Printf("%s watcher register failed for %s: %v", spec.prefix(), changed, err)
					}
				}
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.prune(changed)
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
			timer = time.AfterFunc(delay, func() {
				if err := spec.OnDebounce(ctx); err != nil {
					log.Printf("%s reload failed: %v", spec.prefix(), err)
				}
			})
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
