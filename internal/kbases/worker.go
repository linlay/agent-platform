package kbases

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/knowledge"
	"agent-platform/internal/watch"
	"github.com/fsnotify/fsnotify"
)

type sourceTask struct {
	mu                sync.Mutex
	fingerprint       string
	vectorFingerprint string
	cancel            context.CancelFunc
	watcher           *watch.Watcher
	paths             map[string]map[string]bool
	full              bool
	dirty             time.Time
	last              time.Time
}

func (t *sourceTask) changed(collection, p string, full bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dirty.IsZero() {
		t.dirty = time.Now()
	}
	t.full = t.full || full
	if p != "" {
		if t.paths[collection] == nil {
			t.paths[collection] = map[string]bool{}
		}
		t.paths[collection][p] = true
		if len(t.paths[collection]) > 4096 {
			t.full = true
		}
	}
}

// Start owns one scheduler per deployment and one watcher per library. Definition
// files are read on each scan, so edits outside the admin API are reconciled too.
func (s *Service) Start() error {
	s.admission.Lock()
	defer s.admission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	lock, err := os.OpenFile(filepath.Join(s.runtimeRoot, ".worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = lockWorker(lock); err != nil {
		lock.Close()
		return fmt.Errorf("knowledge library scheduler lock: %w", err)
	}
	s.started = true
	debounce := s.options.Debounce
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	interval := s.options.ReconcileInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer lock.Close()
		ticker := time.NewTicker(debounce)
		defer ticker.Stop()
		defer s.tasks.Range(func(_, v any) bool {
			t := v.(*sourceTask)
			t.cancel()
			if t.watcher != nil {
				<-t.watcher.Done()
			}
			return true
		})
		for {
			if s.ctx.Err() != nil {
				return
			}
			s.scanTasks(debounce, interval)
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
func (s *Service) scanTasks(debounce, interval time.Duration) {
	definitions, err := s.List()
	if err != nil {
		log.Printf("[kbases] reconcile: %v", err)
		return
	}
	seen := map[string]bool{}
	for _, d := range definitions {
		if d.Orphaned || d.InvalidID || len(d.Collections) == 0 {
			continue
		}
		// Revalidate sources even for diagnostic list entries.
		lock := s.libraryLock(d.ID)
		lock.RLock()
		desired, e := s.loadConfiguration(d.ID, true)
		lock.RUnlock()
		if e != nil {
			continue
		}
		d = desired
		seen[d.ID] = true
		fingerprints := s.fingerprints(d)
		fingerprint := fingerprints.Source
		existing, ok := s.tasks.Load(d.ID)
		if ok && existing.(*sourceTask).fingerprint != fingerprint {
			old := existing.(*sourceTask)
			old.cancel()
			if old.watcher != nil {
				<-old.watcher.Done()
			}
			s.tasks.Delete(d.ID)
			ok = false
		}
		var task *sourceTask
		if !ok {
			task = s.newTask(d, fingerprint)
			task.vectorFingerprint = fingerprints.Vector
			s.tasks.Store(d.ID, task)
		} else {
			task = existing.(*sourceTask)
		}
		task.mu.Lock()
		if task.vectorFingerprint != fingerprints.Vector && task.dirty.IsZero() && !s.isBusy(d.ID) {
			if _, err := s.refreshWithMode(d.ID, nil, true); err == nil {
				task.vectorFingerprint = fingerprints.Vector
			}
			task.mu.Unlock()
			continue
		}
		if time.Since(task.last) >= interval && task.dirty.IsZero() {
			task.dirty = time.Now()
			task.full = true
		}
		if task.dirty.IsZero() || time.Since(task.dirty) < debounce || s.isBusy(d.ID) {
			task.mu.Unlock()
			continue
		}
		changes := map[string][]string{}
		if task.full {
			changes = nil
		} else {
			for c, paths := range task.paths {
				for p := range paths {
					changes[c] = append(changes[c], p)
				}
			}
		}
		// Holding the event mutex until enqueue preserves changes arriving during an update.
		_, err = s.refresh(d.ID, changes)
		if err == nil {
			task.paths = map[string]map[string]bool{}
			task.full = false
			task.dirty = time.Time{}
			task.last = time.Now()
			task.vectorFingerprint = fingerprints.Vector
		} else if err != ErrBusy {
			task.last = time.Now()
			task.dirty = time.Time{}
			task.full = true
		}
		task.mu.Unlock()
	}
	s.tasks.Range(func(key, value any) bool {
		if !seen[key.(string)] {
			t := value.(*sourceTask)
			t.cancel()
			if t.watcher != nil {
				<-t.watcher.Done()
			}
			s.tasks.Delete(key)
		}
		return true
	})
}
func (s *Service) newTask(d Definition, fingerprint string) *sourceTask {
	ctx, cancel := context.WithCancel(s.ctx)
	t := &sourceTask{fingerprint: fingerprint, cancel: cancel, paths: map[string]map[string]bool{}, full: true, dirty: time.Now()}
	roots := []watch.Root{}
	ignored := func(p string) bool {
		if canonicalWithin(s.runtimeRoot, p) || (s.options.StateDir != "" && canonicalWithin(s.options.StateDir, p)) {
			return true
		}
		for _, c := range d.Collections {
			rel, err := filepath.Rel(c.SourcePath, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if rel == "." {
				return false
			}
			for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
				if strings.HasPrefix(part, ".") || part == "node_modules" || part == "vendor" || part == "build" || part == "dist" {
					return true
				}
			}
		}
		return false
	}
	for _, c := range d.Collections {
		roots = append(roots, watch.Root{Path: c.SourcePath, Recursive: true, ShouldTraverse: func(p string) bool {
			st, err := os.Lstat(p)
			return err == nil && st.Mode()&os.ModeSymlink == 0 && !ignored(p)
		}})
	}
	watcher, err := watch.Start(ctx, watch.Spec{LogPrefix: "[kbases]", Roots: roots, Ignore: ignored, OnEvent: func(event watch.Event) {
		for _, c := range effectiveCollections(d.Collections) {
			rel, err := filepath.Rel(c.SourcePath, event.Path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			full := event.Op&(fsnotify.Remove|fsnotify.Rename) != 0
			if st, err := os.Lstat(event.Path); err == nil {
				full = full || st.IsDir() || st.Mode()&os.ModeSymlink != 0
			}
			if full {
				t.changed(c.Name, "", true)
			} else if knowledge.IndexedPathAllowed(filepath.ToSlash(rel), c.Include, c.Exclude) {
				t.changed(c.Name, filepath.ToSlash(rel), false)
			}
		}
	}, OnError: func(err error) {
		log.Printf("[kbases] watcher %s: %v; periodic reconciliation remains active", d.ID, err)
		t.changed("", "", true)
	}})
	if err != nil {
		log.Printf("[kbases] watcher %s: %v; periodic reconciliation remains active", d.ID, err)
	}
	t.watcher = watcher
	return t
}
