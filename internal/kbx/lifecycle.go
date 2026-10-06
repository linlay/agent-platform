package kbx

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/kbase"
	"agent-platform/internal/operationstate"
	"agent-platform/internal/watch"
	"github.com/fsnotify/fsnotify"
)

type refreshReceipt struct {
	Result   kbase.RefreshResult `json:"result"`
	Database string              `json:"database"`
	Force    bool                `json:"force"`
}
type refreshJob struct {
	receipt refreshReceipt
	paths   []string
}
type libraryWorker struct {
	mu          sync.Mutex
	library     library
	ctx         context.Context
	cancel      context.CancelFunc
	wake        chan struct{}
	queue       []refreshJob
	paths       map[string]bool
	full        bool
	dirtyAt     time.Time
	running     bool
	initialized bool
	latest      string
	lastError   string
	watchError  string
	indexedAt   *int64
	index       *indexState
}

func (m *Manager) receiptRoot() string { return filepath.Join(m.options.StateDir, "kbx", "refresh") }
func (m *Manager) Start(parent context.Context) {
	m.mu.Lock()
	if m.ctx != nil || m.closed {
		m.mu.Unlock()
		return
	}
	m.ctx, m.cancel = context.WithCancel(parent)
	// One Platform scheduler per runtime; KBX additionally serializes individual writes.
	root := filepath.Join(m.options.StateDir, "kbx")
	err := os.MkdirAll(root, 0700)
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(filepath.Join(root, "agent-worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	}
	if err == nil {
		err = lockWorker(f)
	}
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		m.startError = fmt.Errorf("KBX scheduler lock: %w", err)
		m.mu.Unlock()
		return
	}
	m.lockFile = f
	if err = m.recoverReceipts(); err != nil {
		m.startError = err
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.ReconcileWatchers(parent)
}
func (m *Manager) recoverReceipts() error {
	entries, err := os.ReadDir(m.receiptRoot())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := os.ReadFile(filepath.Join(m.receiptRoot(), entry.Name()))
		if e != nil {
			return e
		}
		var r refreshReceipt
		if e = json.Unmarshal(b, &r); e != nil {
			return fmt.Errorf("invalid KBX refresh receipt: %w", e)
		}
		if r.Result.Status == "pending" || r.Result.Status == "running" {
			r.Result.Status = "interrupted"
			r.Result.Error = "Platform restarted; source reconciliation will run as a new operation"
			if e = operationstate.Write(m.receiptRoot(), r.Result.RefreshID, r); e != nil {
				return e
			}
		}
	}
	return nil
}
func (m *Manager) ReconcileWatchers(_ context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.closed || m.startError != nil || m.ctx.Err() != nil {
		return
	}
	wanted := map[string]library{}
	if m.agents != nil {
		for _, a := range m.agents.Agents() {
			l, e := m.resolve(a.Key)
			if e == nil {
				wanted[a.Key] = l
			}
		}
	}
	for key, w := range m.workers {
		l, ok := wanted[key]
		if !ok || l.database != w.library.database {
			w.cancel()
			delete(m.workers, key)
		}
	}
	for key, l := range wanted {
		if m.workers[key] != nil {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		w := &libraryWorker{library: l, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), paths: map[string]bool{}}
		m.workers[key] = w
		m.wg.Add(1)
		go func() { defer m.wg.Done(); m.runLibrary(w) }()
	}
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		m.mu.Lock()
		if m.lockFile != nil {
			_ = m.lockFile.Close()
			m.lockFile = nil
		}
		m.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) Refresh(ctx context.Context, key string, o kbase.RefreshOptions) (kbase.RefreshResult, error) {
	if err := ctx.Err(); err != nil {
		return kbase.RefreshResult{}, err
	}
	l, err := m.resolve(key)
	if err != nil {
		return kbase.RefreshResult{}, err
	}
	if len(o.Paths) > 0 || (o.Scope != "" && o.Scope != "full") {
		return kbase.RefreshResult{}, fmt.Errorf("manual KBX refresh requires full scope")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.workers[key]
	if m.closed || m.ctx == nil || m.ctx.Err() != nil || m.startError != nil || w == nil || w.library.database != l.database {
		return kbase.RefreshResult{}, unavailable("KBX scheduler is unavailable; inspect Platform startup and builtin capabilities")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) >= 256 {
		return kbase.RefreshResult{}, unavailable("KBX refresh queue is full")
	}
	if o.Mode == "" {
		o.Mode = "manual"
	}
	r := refreshReceipt{Result: kbase.RefreshResult{RefreshID: rand.Text(), AgentKey: key, Mode: o.Mode, Scope: "full", Status: "pending"}, Database: l.database, Force: o.Force}
	if err = operationstate.Write(m.receiptRoot(), r.Result.RefreshID, r); err != nil {
		return kbase.RefreshResult{}, unavailable("cannot persist KBX refresh receipt")
	}
	w.queue = append(w.queue, refreshJob{receipt: r})
	w.latest = r.Result.RefreshID
	w.signal()
	return r.Result, nil
}
func (m *Manager) RefreshOperationStatus(key, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("refreshId is required")
	}
	var r refreshReceipt
	if err := operationstate.Read(m.receiptRoot(), id, &r); err != nil {
		return "", unavailable("KBX refresh receipt not found or unreadable")
	}
	if r.Result.AgentKey != key || r.Result.RefreshID != id {
		return "", fmt.Errorf("KBX refresh does not belong to this Agent")
	}
	return r.Result.Status, nil
}
func (w *libraryWorker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *libraryWorker) changed(path string, full bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx.Err() != nil {
		return
	}
	if w.dirtyAt.IsZero() {
		w.dirtyAt = time.Now()
	}
	w.full = w.full || full
	if path != "" {
		w.paths[path] = true
	}
	if len(w.paths) > 4096 {
		w.full = true
		clear(w.paths)
	}
	w.signal()
}
func (m *Manager) runLibrary(w *libraryWorker) {
	// Subscribe before the initial scan, so edits during that scan remain queued.
	ignored := func(p string) bool {
		rel, e := filepath.Rel(w.library.spec.WorkspaceRoot, p)
		if e != nil {
			return true
		}
		if rel == "." {
			return false
		}
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if strings.HasPrefix(part, ".") || part == "node_modules" || part == "vendor" || part == "dist" || part == "build" {
				return true
			}
		}
		for _, root := range []string{filepath.Dir(w.library.database), m.options.StateDir} {
			r, e := filepath.Rel(root, p)
			if e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	watcher, e := watch.Start(w.ctx, watch.Spec{LogPrefix: "[kbx]", Roots: []watch.Root{{Path: w.library.spec.WorkspaceRoot, Recursive: true, ShouldTraverse: func(p string) bool {
		if ignored(p) {
			return false
		}
		s, e := os.Lstat(p)
		return e == nil && s.Mode()&os.ModeSymlink == 0
	}}}, Ignore: ignored,
		OnEvent: func(e watch.Event) {
			rel, err := filepath.Rel(w.library.spec.WorkspaceRoot, e.Path)
			if err != nil {
				return
			}
			full := e.Op&(fsnotify.Remove|fsnotify.Rename) != 0
			if s, err := os.Lstat(e.Path); err == nil {
				full = full || s.IsDir()
				if s.Mode()&os.ModeSymlink != 0 {
					full = true
				}
			}
			if full {
				w.changed("", true)
			} else if kbase.IndexedPathAllowed(filepath.ToSlash(rel), w.library.spec.Config.Include, w.library.spec.Config.Exclude) {
				w.changed(filepath.ToSlash(rel), false)
			}
		}, OnError: func(err error) {
			w.mu.Lock()
			w.watchError = "directory watcher failed; periodic reconciliation remains active"
			w.mu.Unlock()
			w.changed("", true)
		},
	})
	if e != nil {
		w.mu.Lock()
		w.watchError = "directory watcher unavailable; periodic reconciliation remains active"
		w.mu.Unlock()
	}
	defer func() {
		w.cancel()
		if watcher != nil {
			<-watcher.Done()
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, j := range w.queue {
			m.finishReceipt(&j.receipt, "canceled", "Agent scope retired or Platform stopped")
		}
	}()
	w.changed("", true)
	tick := time.NewTicker(m.options.ReconcileInterval)
	defer tick.Stop()
	debounce := time.NewTicker(m.options.Debounce)
	defer debounce.Stop()
	for {
		if w.ctx.Err() != nil {
			return
		}
		select {
		case <-tick.C:
			w.changed("", true)
		default:
		}
		var job *refreshJob
		w.mu.Lock()
		if len(w.queue) > 0 {
			j := w.queue[0]
			w.queue = w.queue[1:]
			// This full scan covers changes observed before dequeue; later events
			// remain in the next batch instead of being cleared on completion.
			w.full = false
			clear(w.paths)
			w.dirtyAt = time.Time{}
			job = &j
		} else if !w.dirtyAt.IsZero() && time.Since(w.dirtyAt) >= m.options.Debounce {
			paths := []string{}
			for p := range w.paths {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			scope := "paths"
			if w.full || !w.initialized {
				scope = "full"
				paths = nil
			}
			r := refreshReceipt{Result: kbase.RefreshResult{RefreshID: rand.Text(), AgentKey: w.library.spec.Key, Mode: "background", Scope: scope, Status: "pending", CandidatePaths: len(paths)}, Database: w.library.database}
			if err := operationstate.Write(m.receiptRoot(), r.Result.RefreshID, r); err != nil {
				w.lastError = "cannot persist KBX refresh receipt"
			} else {
				job = &refreshJob{receipt: r, paths: paths}
				w.full = false
				clear(w.paths)
				w.dirtyAt = time.Time{}
			}
		}
		if job != nil {
			w.running = true
			w.latest = job.receipt.Result.RefreshID
		}
		w.mu.Unlock()
		if job == nil {
			select {
			case <-w.ctx.Done():
				return
			case <-tick.C:
				w.changed("", true)
			case <-w.wake:
			case <-debounce.C:
			}
			continue
		}
		m.executeJob(w, job)
	}
}
func (m *Manager) finishReceipt(r *refreshReceipt, status, message string) error {
	r.Result.Status = status
	r.Result.Error = message
	err := operationstate.Write(m.receiptRoot(), r.Result.RefreshID, r)
	if err != nil {
		log.Printf("[kbx] cannot persist refresh outcome for %s: %v", r.Result.AgentKey, err)
	}
	return err
}
func (m *Manager) executeJob(w *libraryWorker, j *refreshJob) {
	var err error
	select {
	case m.gate <- struct{}{}:
		defer func() { <-m.gate }()
	case <-w.ctx.Done():
		err = w.ctx.Err()
	}
	if err == nil {
		err = m.finishReceipt(&j.receipt, "running", "")
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(w.ctx, m.options.MaintenanceTimeout)
		err = m.performRefresh(ctx, w, j)
		cancel()
	}
	state, message := "completed", ""
	if err != nil {
		state = "failed"
		message = err.Error()
	}
	if w.ctx.Err() != nil {
		state = "interrupted"
		message = "Platform stopped or Agent scope changed; completion requires reconciliation"
	}
	if saveErr := m.finishReceipt(&j.receipt, state, message); saveErr != nil {
		message = "cannot persist KBX terminal receipt"
	}
	w.mu.Lock()
	w.running = false
	w.lastError = message
	if message == "" {
		now := time.Now().UnixMilli()
		w.indexedAt = &now
		w.initialized = true
	}
	w.mu.Unlock()
}
