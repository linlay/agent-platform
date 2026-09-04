package mcp

import (
	"context"
	"log"
	"sync"
	"time"

	"agent-platform/internal/contracts"
)

// SyncCoordinator owns initial MCP discovery, registry-triggered refreshes,
// and availability-gate retries. All remote and stdio work runs on its single
// background worker so lifecycle paths only need to publish local state and
// enqueue work.
type SyncCoordinator struct {
	registry      *Registry
	sync          *ToolSync
	gate          *AvailabilityGate
	interval      time.Duration
	notifications contracts.NotificationSink
	trigger       chan struct{}

	mu            sync.Mutex
	started       bool
	activeAttempt uint64
	activeCancel  context.CancelFunc
}

func NewSyncCoordinator(registry *Registry, syncer *ToolSync, gate *AvailabilityGate, interval time.Duration, notifications ...contracts.NotificationSink) *SyncCoordinator {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	var sink contracts.NotificationSink
	if len(notifications) > 0 {
		sink = notifications[0]
	}
	return &SyncCoordinator{
		registry:      registry,
		sync:          syncer,
		gate:          gate,
		interval:      interval,
		notifications: sink,
		trigger:       make(chan struct{}, 1),
	}
}

// NewReconnectLoop is retained as the compatibility constructor for focused
// MCP tests and callers. The loop now also performs the initial asynchronous
// synchronization and accepts explicit refresh requests.
func NewReconnectLoop(registry *Registry, syncer *ToolSync, gate *AvailabilityGate, interval time.Duration, notifications ...contracts.NotificationSink) *SyncCoordinator {
	return NewSyncCoordinator(registry, syncer, gate, interval, notifications...)
}

// Start launches the worker and returns without contacting any MCP server.
func (c *SyncCoordinator) Start(ctx context.Context) {
	if c == nil || c.registry == nil || c.sync == nil || c.gate == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()
	go c.run(ctx)
	c.enqueue()
}

// ScheduleSync cancels an obsolete in-flight attempt and coalesces a full
// refresh request for the latest registry version.
func (c *SyncCoordinator) ScheduleSync() {
	if c == nil {
		return
	}
	c.mu.Lock()
	started := c.started
	cancel := c.activeCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !started {
		return
	}
	c.enqueue()
}

func (c *SyncCoordinator) run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.trigger:
			c.runSync(ctx, nil)
		case <-ticker.C:
			keys := c.serverKeys()
			due := c.gate.ReadyToRetry(keys)
			if len(due) > 0 {
				c.runSync(ctx, due)
			}
		}
	}
}

func (c *SyncCoordinator) runSync(parent context.Context, serverKeys []string) {
	if parent.Err() != nil {
		return
	}
	ctx, finish := c.beginAttempt(parent)
	defer finish()
	startedAt := time.Now()
	if c.sync.client != nil {
		c.sync.client.Reconcile()
	}
	var (
		result ToolSyncResult
		err    error
	)
	if len(serverKeys) == 0 {
		result, err = c.sync.refreshTools(ctx, nil)
	} else {
		result, err = c.sync.RefreshServersWithResult(ctx, serverKeys)
	}
	if err != nil {
		if parent.Err() == nil && ctx.Err() == nil {
			log.Printf("[mcp] background tool synchronization failed: %v", err)
		}
		return
	}
	if result.Stale {
		c.enqueue()
		return
	}
	if result.Changed {
		c.broadcastUpdate()
	}
	log.Printf("[mcp] background tool synchronization completed in %s", time.Since(startedAt).Round(time.Millisecond))
}

func (c *SyncCoordinator) beginAttempt(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.activeAttempt++
	attempt := c.activeAttempt
	c.activeCancel = cancel
	c.mu.Unlock()
	return ctx, func() {
		cancel()
		c.mu.Lock()
		if c.activeAttempt == attempt {
			c.activeCancel = nil
		}
		c.mu.Unlock()
	}
}

func (c *SyncCoordinator) enqueue() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

func (c *SyncCoordinator) serverKeys() []string {
	servers := c.registry.Servers()
	keys := make([]string, 0, len(servers))
	for _, server := range servers {
		keys = append(keys, server.Key)
	}
	return keys
}

func (c *SyncCoordinator) broadcastUpdate() {
	if c.notifications == nil {
		return
	}
	c.notifications.Broadcast("catalog.updated", map[string]any{
		"reason":    "mcp-servers",
		"updatedAt": time.Now().UnixMilli(),
	})
}
