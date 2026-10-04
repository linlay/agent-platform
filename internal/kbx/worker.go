package kbx

import (
	"context"
	"fmt"
	"sync"
)

// UpdateFunc is supplied by the versioned KBX maintenance protocol adapter.
// The CLI owns the per-library singleton lock and maintenance execution. This
// worker only owns the connection; it does not run embed or rebuild indexes.
type UpdateFunc func(context.Context, string) error

// Workers owns long-lived update connections, independently of HTTP/Run waits.
// It deliberately does not guess when a disconnected update is complete. A
// subsequent Start reconnects using the same canonical knowledge-base identity.
type Workers struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	update  UpdateFunc
	entries map[string]*updateConnection
	closed  bool
	wg      sync.WaitGroup
}
type updateConnection struct {
	done chan struct{}
	err  error
}

func NewWorkers(parent context.Context, update UpdateFunc) *Workers {
	ctx, cancel := context.WithCancel(parent)
	return &Workers{ctx: ctx, cancel: cancel, update: update, entries: map[string]*updateConnection{}}
}

// Start attaches to the existing local worker, or starts a new connection. A
// finished entry is replaced only on an explicit Start, never by Wait.
func (w *Workers) Start(library string) error {
	if library == "" {
		return fmt.Errorf("knowledge-base identity is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("KBX workers are closed")
	}
	if w.update == nil {
		return fmt.Errorf("KBX update protocol adapter is not configured")
	}
	if old := w.entries[library]; old != nil {
		select {
		case <-old.done:
		default:
			return nil
		}
	}
	entry := &updateConnection{done: make(chan struct{})}
	w.entries[library] = entry
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		entry.err = w.update(w.ctx, library)
		close(entry.done)
	}()
	return nil
}

// Wait cancels only this subscriber. A disconnected HTTP request or Agent Run
// must not cancel the update connection shared by the knowledge base.
func (w *Workers) Wait(ctx context.Context, library string) error {
	w.mu.Lock()
	entry := w.entries[library]
	w.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("KBX update connection not started")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-entry.done:
		return entry.err
	}
}
func (w *Workers) Close(ctx context.Context) error {
	w.mu.Lock()
	w.closed = true
	w.cancel()
	w.mu.Unlock()
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
