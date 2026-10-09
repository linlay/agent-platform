package kbasescenter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/knowledge"
	"agent-platform/internal/pathutil"
)

// Options are deployment-owned; an Agent binding never changes source permissions.
type Options struct {
	ChatsDir, StateDir, RuntimeDir string
	References                     func(string) []string
	Debounce, ReconcileInterval    time.Duration
}

type ReferencedError struct{ Agents []string }

func (e *ReferencedError) Error() string {
	return "knowledge base is referenced by Agents: " + strings.Join(e.Agents, ", ")
}
func ValidID(id string) bool { return id != "libraries" && idPattern.MatchString(id) }
func (s *Service) libraryLock(id string) *sync.RWMutex {
	value, _ := s.locks.LoadOrStore(id, &sync.RWMutex{})
	return value.(*sync.RWMutex)
}
func (s *Service) isBusy(id string) bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.busy[id] }
func (s *Service) setBusy(id string, busy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if busy {
		s.busy[id] = true
	} else {
		delete(s.busy, id)
	}
}
func (s *Service) validateSource(source string) error {
	if err := knowledge.ValidateWorkspaceChatsSeparation(source, s.options.ChatsDir); err != nil {
		return err
	}
	for _, root := range []string{s.root, s.runtimeRoot, s.options.StateDir} {
		if root != "" && canonicalWithin(root, source) {
			return fmt.Errorf("knowledge source must not overlap kbases, ru-kbases or the protected state directory")
		}
	}
	return nil
}
func effectiveCollections(input []Collection) []Collection {
	out := append([]Collection(nil), input...)
	for i := range out {
		c := &out[i]
		if c.Include == nil {
			c.Include = knowledge.DefaultIncludePatterns()
		}
		if c.Exclude == nil {
			c.Exclude = knowledge.DefaultExcludePatterns()
		}
		if c.Chunk.Unit == "" {
			c.Chunk = knowledge.DefaultChunkConfig()
		}
	}
	return out
}
func (s *Service) effectiveCollections(input []Collection) []Collection {
	out := effectiveCollections(input)
	for i := range out {
		c := &out[i]
		c.Exclude = append([]string(nil), c.Exclude...)
		for _, root := range []string{s.root, s.runtimeRoot, s.options.RuntimeDir, s.options.StateDir} {
			if root == "" {
				continue
			}
			rel, err := filepath.Rel(c.SourcePath, root)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				c.Exclude = append(c.Exclude, filepath.ToSlash(rel)+"/**")
			}
		}
	}
	return out
}

// Acquire pins a library against config edits/deletion for one read. Maintenance
// uses the same read lease: KBX owns index read/write concurrency, not a global mutex.
func (s *Service) Acquire(id string) (Definition, string, func(), error) {
	lock := s.libraryLock(id)
	lock.RLock()
	fail := func(d Definition, err error) (Definition, string, func(), error) {
		lock.RUnlock()
		return d, "", nil, err
	}
	d, err := s.load(id)
	if err != nil {
		return fail(d, err)
	}
	if d.IndexedAt == 0 {
		return fail(d, fmt.Errorf("knowledge library %s is not ready (%s)", id, d.State))
	}
	dir, err := s.runtimeDirectory(id, false)
	if err != nil {
		return fail(d, err)
	}
	db := filepath.Join(dir, "index.sqlite")
	if st, err := os.Lstat(db); err != nil || !st.Mode().IsRegular() {
		return fail(d, fmt.Errorf("knowledge index is unavailable"))
	}
	d.Collections = s.effectiveCollections(d.Collections)
	return d, db, lock.RUnlock, nil
}

// ReadableFailure is only returned after the engine confirmed a readable text
// index for every collection. Unknown/partial mutations must not use this type.
type ReadableFailure struct{ Err error }

func (e *ReadableFailure) Error() string { return e.Err.Error() }
func (e *ReadableFailure) Unwrap() error { return e.Err }

type IncrementalEngine interface {
	UpdatePaths(context.Context, string, []Collection, map[string][]string) error
}

func (s *Service) Refresh(id string) (Definition, error) { return s.refresh(id, nil) }
func (s *Service) refresh(id string, changes map[string][]string) (Definition, error) {
	s.admission.Lock()
	defer s.admission.Unlock()
	lock := s.libraryLock(id)
	if !lock.TryLock() {
		return Definition{}, ErrBusy
	}
	defer lock.Unlock()
	if s.ctx.Err() != nil {
		return Definition{}, s.ctx.Err()
	}
	if _, held := s.held.Load(id); held {
		return Definition{}, ErrBusy
	}
	if s.isBusy(id) {
		return Definition{}, ErrBusy
	}
	d, err := s.loadConfiguration(id, false)
	if err != nil {
		return d, err
	}
	if s.engine == nil {
		return d, fmt.Errorf("KBX engine unavailable")
	}
	dir, err := s.runtimeDirectory(id, true)
	if err != nil {
		return d, err
	}
	state, err := s.readState(id)
	if errors.Is(err, errInvalidRuntimeState) {
		state, err = runtimeState{}, nil
	}
	if err != nil {
		return d, err
	}
	previous := state
	fingerprint := s.fingerprint(d.Collections)
	same := state.AppliedFingerprint == fingerprint && state.IndexedAt > 0 && state.State != "indexing"
	// A previous embedding failure may belong to a different collection from
	// this path batch. Reconcile every collection before clearing degraded.
	if state.Degraded {
		changes = nil
	}
	if !same {
		state.IndexedAt = 0
		state.AppliedFingerprint = ""
		changes = nil
	}
	state.TaskFingerprint = fingerprint
	state.State = "indexing"
	state.Error = ""
	state.RefreshError = ""
	state.UpdatedAt = time.Now().UnixMilli()
	if state.CreatedAt == 0 {
		state.CreatedAt = d.CreatedAt
	}
	if err = s.saveState(id, state); err != nil {
		return d, err
	}
	s.setBusy(id, true)
	d.State = "indexing"
	d.Indexing = true
	d.Stale = true
	d.IndexedAt = state.IndexedAt
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, 35*time.Minute)
		defer cancel()
		select {
		case s.gate <- struct{}{}:
			defer func() { <-s.gate }()
		case <-ctx.Done():
			s.setBusy(id, false)
			return
		}
		// read lease permits queries but protects paths from Edit/Delete.
		lock.RLock()
		collections := s.effectiveCollections(d.Collections)
		if engine, ok := s.engine.(IncrementalEngine); ok {
			err = engine.UpdatePaths(ctx, filepath.Join(dir, "index.sqlite"), collections, changes)
		} else {
			err = s.engine.Update(ctx, filepath.Join(dir, "index.sqlite"), collections)
		}
		lock.RUnlock()
		lock.Lock()
		defer lock.Unlock()
		defer s.setBusy(id, false)
		state.UpdatedAt = time.Now().UnixMilli()
		var readable *ReadableFailure
		switch {
		case errors.Is(err, ErrNotStarted):
			state = previous
			if state.State == "" {
				state.State = "unindexed"
			}
			state.UpdatedAt = time.Now().UnixMilli()
			state.RefreshError = err.Error()
		case errors.As(err, &readable):
			state.State = "ready"
			state.IndexedAt = state.UpdatedAt
			state.AppliedFingerprint = fingerprint
			state.Degraded = true
			state.RefreshError = err.Error()
		case err != nil:
			state.State = "error"
			state.Error = err.Error()
			state.IndexedAt = 0
			state.AppliedFingerprint = ""
		default:
			state.State = "ready"
			state.IndexedAt = state.UpdatedAt
			state.AppliedFingerprint = fingerprint
			state.Degraded = false
		}
		if saveErr := s.saveState(id, state); saveErr != nil {
			log.Printf("[kbases] persist %s: %v", id, saveErr)
		}
	}()
	return d, nil
}
func (s *Service) Close(ctx context.Context) error {
	s.admission.Lock()
	s.cancel()
	s.admission.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func canonicalWithin(root, p string) bool {
	a, err := pathutil.Canonicalize(root)
	if err != nil {
		return false
	}
	b, err := pathutil.Canonicalize(p)
	return err == nil && pathutil.WithinRoot(a, b)
}
