package kbase

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

const maxDeltaPaths = 1000

type deltaQueue struct {
	agentKey  string
	paths     map[string]struct{}
	reconcile bool
	running   bool
	retries   int
}

type refreshResolver interface {
	Resolve(agentKey string) (resolvedConfig, *Embedder, error)
}

type generationCoordinatorBackend interface {
	Refresh(ctx context.Context, cfg resolvedConfig, embedder *Embedder, options RefreshOptions, pendingChanges func() int) (RefreshResult, error)
	Rollback(ctx context.Context, cfg resolvedConfig, generationID string) (*Generation, error)
	ReleaseStorageGeneration(agentKey, storageDir string)
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

type refreshCoordinator struct {
	resolver    refreshResolver
	state       *capabilityState
	generations generationCoordinatorBackend

	mu             sync.Mutex
	wg             sync.WaitGroup
	locks          map[string]*sync.Mutex
	running        map[string]bool
	storageRunning map[string]bool
	storageQueued  map[string]bool
	deltaQueues    map[string]*deltaQueue
	closing        bool
}

func newRefreshCoordinator(resolver refreshResolver, state *capabilityState, generations generationCoordinatorBackend) *refreshCoordinator {
	return &refreshCoordinator{
		resolver: resolver, state: state, generations: generations,
		locks: map[string]*sync.Mutex{}, running: map[string]bool{}, storageRunning: map[string]bool{},
		storageQueued: map[string]bool{}, deltaQueues: map[string]*deltaQueue{},
	}
}

func (c *refreshCoordinator) Refresh(ctx context.Context, agentKey string, options RefreshOptions) (RefreshResult, error) {
	if err := c.beginOperation(); err != nil {
		return failedRefresh(agentKey, options.Mode, err), err
	}
	defer c.wg.Done()

	cfg, embedder, err := c.resolver.Resolve(agentKey)
	if err != nil {
		return RefreshResult{AgentKey: agentKey, Status: "failed", Error: err.Error()}, err
	}
	storageKey := storageLockKey(cfg.StorageDir)
	lock := c.storageLock(storageKey)
	lock.Lock()
	defer lock.Unlock()
	c.setRunning(cfg.AgentKey, storageKey, true)
	defer c.setRunning(cfg.AgentKey, storageKey, false)

	result, err := c.generations.Refresh(ctx, cfg, embedder, options, func() int { return c.PendingChanges(cfg.StorageDir) })
	if err == nil {
		c.state.ClearFailure(cfg.AgentKey)
	}
	return result, err
}

func (c *refreshCoordinator) Rollback(ctx context.Context, agentKey, generationID string) (*Generation, error) {
	if err := c.beginOperation(); err != nil {
		return nil, err
	}
	defer c.wg.Done()
	cfg, _, err := c.resolver.Resolve(agentKey)
	if err != nil {
		return nil, err
	}
	lock := c.storageLock(storageLockKey(cfg.StorageDir))
	lock.Lock()
	defer lock.Unlock()
	return c.generations.Rollback(ctx, cfg, generationID)
}

func (c *refreshCoordinator) beginOperation() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return &PolicyError{Kind: ErrorUnavailable, Message: "KBASE manager is shutting down"}
	}
	c.wg.Add(1)
	return nil
}

func (c *refreshCoordinator) QueueRefresh(agentKey, storageDir, mode string) {
	storageKey := storageLockKey(storageDir)
	c.mu.Lock()
	if c.closing || c.storageRunning[storageKey] || c.storageQueued[storageKey] {
		c.mu.Unlock()
		return
	}
	c.storageQueued[storageKey] = true
	c.mu.Unlock()
	go func() {
		defer c.setStorageQueued(storageKey, false)
		if _, err := c.Refresh(context.Background(), agentKey, RefreshOptions{Mode: mode}); err != nil {
			log.Printf("[kbase] %s refresh failed agent=%s: %v", mode, agentKey, err)
		}
	}()
}

func (c *refreshCoordinator) QueueDelta(agentKey, storageDir string, paths []string, reconcile bool) {
	key := storageLockKey(storageDir)
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return
	}
	queue := c.deltaQueues[key]
	if queue == nil {
		queue = &deltaQueue{agentKey: strings.TrimSpace(agentKey), paths: map[string]struct{}{}}
		c.deltaQueues[key] = queue
	}
	queue.agentKey = strings.TrimSpace(agentKey)
	queue.merge(paths, reconcile)
	if queue.running || !queue.hasPending() {
		c.mu.Unlock()
		return
	}
	queue.running = true
	c.mu.Unlock()
	go c.runDeltaQueue(key)
}

func (c *refreshCoordinator) runDeltaQueue(storageKey string) {
	for {
		c.mu.Lock()
		queue := c.deltaQueues[storageKey]
		if queue == nil || c.closing {
			if queue != nil {
				queue.running = false
			}
			c.mu.Unlock()
			return
		}
		agentKey := queue.agentKey
		paths, reconcile := queue.take()
		c.mu.Unlock()

		options := RefreshOptions{Mode: "watcher", Scope: "delta", Paths: compactChangedPaths(paths)}
		if reconcile {
			options.Scope, options.Paths = "reconcile", nil
		}
		_, err := c.Refresh(context.Background(), agentKey, options)
		if err != nil {
			c.mu.Lock()
			queue = c.deltaQueues[storageKey]
			if queue == nil {
				c.mu.Unlock()
				return
			}
			queue.merge(paths, reconcile)
			queue.retries++
			queue.running = false
			retries := queue.retries
			c.mu.Unlock()
			delay := time.Duration(1<<minInt(retries, 4)) * time.Second
			log.Printf("[kbase] delta refresh failed agent=%s retryIn=%s: %v", agentKey, delay, err)
			time.AfterFunc(delay, func() { c.resumeDeltaQueue(storageKey) })
			return
		}

		c.mu.Lock()
		queue = c.deltaQueues[storageKey]
		if queue == nil {
			c.mu.Unlock()
			return
		}
		queue.retries = 0
		if !queue.hasPending() {
			queue.running = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

func (c *refreshCoordinator) resumeDeltaQueue(storageKey string) {
	c.mu.Lock()
	queue := c.deltaQueues[storageKey]
	if queue == nil || queue.running || c.closing || !queue.hasPending() {
		c.mu.Unlock()
		return
	}
	queue.running = true
	c.mu.Unlock()
	go c.runDeltaQueue(storageKey)
}

func (c *refreshCoordinator) IsIndexing(agentKey, storageDir string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	storageKey := storageLockKey(storageDir)
	delta := c.deltaQueues[storageKey]
	return c.running[agentKey] || c.storageRunning[storageKey] || c.storageQueued[storageKey] ||
		delta != nil && (delta.running || delta.reconcile || len(delta.paths) > 0)
}

func (c *refreshCoordinator) PendingChanges(storageDir string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if queue := c.deltaQueues[storageLockKey(storageDir)]; queue != nil {
		return queue.pendingCount()
	}
	return 0
}

func (c *refreshCoordinator) DropStorage(agentKey, storageDir string) {
	c.mu.Lock()
	delete(c.deltaQueues, storageLockKey(storageDir))
	c.mu.Unlock()
	go c.generations.ReleaseStorageGeneration(agentKey, storageDir)
}

func (c *refreshCoordinator) BeginClose() {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
}

func (c *refreshCoordinator) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *refreshCoordinator) storageLock(storageKey string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock := c.locks[storageKey]
	if lock == nil {
		lock = &sync.Mutex{}
		c.locks[storageKey] = lock
	}
	return lock
}

func (c *refreshCoordinator) setRunning(agentKey, storageKey string, running bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if running {
		c.running[agentKey], c.storageRunning[storageKey] = true, true
	} else {
		delete(c.running, agentKey)
		delete(c.storageRunning, storageKey)
	}
}

func (c *refreshCoordinator) setStorageQueued(storageKey string, queued bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if queued {
		c.storageQueued[storageKey] = true
	} else {
		delete(c.storageQueued, storageKey)
	}
}
