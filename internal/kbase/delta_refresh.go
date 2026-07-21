package kbase

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

const maxDeltaPaths = 1000

type deltaAccumulator struct {
	mu        sync.Mutex
	paths     map[string]struct{}
	reconcile bool
}

func newDeltaAccumulator() *deltaAccumulator {
	return &deltaAccumulator{paths: map[string]struct{}{}}
}

func (a *deltaAccumulator) Add(path string) {
	if a == nil {
		return
	}
	path = normalizeIndexedPath(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reconcile {
		return
	}
	a.paths[path] = struct{}{}
	if len(a.paths) > maxDeltaPaths {
		a.reconcile = true
		a.paths = map[string]struct{}{}
	}
}

func (a *deltaAccumulator) RequireReconcile() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.reconcile = true
	a.paths = map[string]struct{}{}
	a.mu.Unlock()
}

func (a *deltaAccumulator) Drain() ([]string, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	paths := make([]string, 0, len(a.paths))
	for path := range a.paths {
		paths = append(paths, path)
	}
	reconcile := a.reconcile
	a.paths = map[string]struct{}{}
	a.reconcile = false
	return compactChangedPaths(paths), reconcile
}

type deltaQueue struct {
	agentKey  string
	paths     map[string]struct{}
	reconcile bool
	running   bool
	retries   int
}

func (q *deltaQueue) merge(paths []string, reconcile bool) {
	if q == nil {
		return
	}
	if q.paths == nil {
		q.paths = map[string]struct{}{}
	}
	if reconcile {
		q.reconcile = true
		q.paths = map[string]struct{}{}
		return
	}
	if q.reconcile {
		return
	}
	for _, path := range paths {
		q.paths[normalizeIndexedPath(path)] = struct{}{}
	}
	if len(q.paths) > maxDeltaPaths {
		q.reconcile = true
		q.paths = map[string]struct{}{}
	}
}

func (q *deltaQueue) take() ([]string, bool) {
	if q == nil {
		return nil, false
	}
	paths := make([]string, 0, len(q.paths))
	for path := range q.paths {
		paths = append(paths, path)
	}
	reconcile := q.reconcile
	q.paths = map[string]struct{}{}
	q.reconcile = false
	return compactChangedPaths(paths), reconcile
}

func (q *deltaQueue) hasPending() bool {
	return q != nil && (q.reconcile || len(q.paths) > 0)
}

func (q *deltaQueue) pendingCount() int {
	if q == nil {
		return 0
	}
	if q.reconcile {
		return 1
	}
	return len(q.paths)
}

func (m *Manager) queueDelta(agentKey, storageDir string, paths []string, reconcile bool) {
	if m == nil {
		return
	}
	key := storageLockKey(storageDir)
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}
	queue := m.deltaQueues[key]
	if queue == nil {
		queue = &deltaQueue{agentKey: strings.TrimSpace(agentKey), paths: map[string]struct{}{}}
		m.deltaQueues[key] = queue
	}
	queue.agentKey = strings.TrimSpace(agentKey)
	queue.merge(paths, reconcile)
	if queue.running || !queue.hasPending() {
		m.mu.Unlock()
		return
	}
	queue.running = true
	m.mu.Unlock()
	go m.runDeltaQueue(key)
}

func (m *Manager) runDeltaQueue(storageKey string) {
	for {
		m.mu.Lock()
		queue := m.deltaQueues[storageKey]
		if queue == nil || m.closing {
			if queue != nil {
				queue.running = false
			}
			m.mu.Unlock()
			return
		}
		agentKey := queue.agentKey
		paths, reconcile := queue.take()
		m.mu.Unlock()

		options := RefreshOptions{Mode: "watcher", Scope: "delta", Paths: compactChangedPaths(paths)}
		if reconcile {
			options.Scope = "reconcile"
			options.Paths = nil
		}
		_, err := m.Refresh(context.Background(), agentKey, options)
		if err != nil {
			m.mu.Lock()
			queue = m.deltaQueues[storageKey]
			if queue == nil {
				m.mu.Unlock()
				return
			}
			queue.merge(paths, reconcile)
			queue.retries++
			queue.running = false
			retries := queue.retries
			m.mu.Unlock()
			delay := time.Duration(1<<minInt(retries, 4)) * time.Second
			log.Printf("[kbase] delta refresh failed agent=%s retryIn=%s: %v", agentKey, delay, err)
			time.AfterFunc(delay, func() { m.resumeDeltaQueue(storageKey) })
			return
		}

		m.mu.Lock()
		queue = m.deltaQueues[storageKey]
		if queue == nil {
			m.mu.Unlock()
			return
		}
		queue.retries = 0
		if !queue.hasPending() {
			queue.running = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func (m *Manager) resumeDeltaQueue(storageKey string) {
	m.mu.Lock()
	queue := m.deltaQueues[storageKey]
	if queue == nil || queue.running || m.closing || !queue.hasPending() {
		m.mu.Unlock()
		return
	}
	queue.running = true
	m.mu.Unlock()
	go m.runDeltaQueue(storageKey)
}

func (m *Manager) pendingChanges(storageDir string) int {
	if m == nil {
		return 0
	}
	key := storageLockKey(storageDir)
	m.mu.Lock()
	defer m.mu.Unlock()
	queue := m.deltaQueues[key]
	if queue == nil {
		return 0
	}
	return queue.pendingCount()
}

func (m *Manager) dropDeltaQueue(storageDir string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.deltaQueues, storageLockKey(storageDir))
	m.mu.Unlock()
}
