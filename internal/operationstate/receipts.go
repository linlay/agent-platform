package operationstate

import "sync"

// Receipts remembers the outcome of each tool invocation for the life of the
// process, so the same invocation is never applied twice. Nothing is written
// to disk: a restart cancels every unfinished Run, and with it every
// invocation identity these receipts are keyed by. Callers must serialize
// compound load/execute/store operations using their service mutation lock.
type Receipts[T any] struct {
	mu    sync.Mutex
	items map[string]T
}

func (r *Receipts[T]) Load(key string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.items[key]
	return value, ok
}

func (r *Receipts[T]) Store(key string, value T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = map[string]T{}
	}
	r.items[key] = value
}

func (r *Receipts[T]) Delete(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, key)
}
