package mcp

import (
	"context"
	"time"

	"agent-platform/internal/contracts"
)

type ReconnectLoop struct {
	registry      *Registry
	sync          *ToolSync
	gate          *AvailabilityGate
	interval      time.Duration
	notifications contracts.NotificationSink
}

func NewReconnectLoop(registry *Registry, sync *ToolSync, gate *AvailabilityGate, interval time.Duration, notifications ...contracts.NotificationSink) *ReconnectLoop {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	var sink contracts.NotificationSink
	if len(notifications) > 0 {
		sink = notifications[0]
	}
	return &ReconnectLoop{registry: registry, sync: sync, gate: gate, interval: interval, notifications: sink}
}

func (r *ReconnectLoop) Start(ctx context.Context) {
	if r == nil || r.registry == nil || r.sync == nil || r.gate == nil {
		return
	}
	ticker := time.NewTicker(r.interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				servers := r.registry.Servers()
				keys := make([]string, 0, len(servers))
				for _, server := range servers {
					keys = append(keys, server.Key)
				}
				due := r.gate.ReadyToRetry(keys)
				if len(due) == 0 {
					continue
				}
				result, _ := r.sync.RefreshServersWithResult(ctx, due)
				if result.Changed && r.notifications != nil {
					r.notifications.Broadcast("catalog.updated", map[string]any{
						"reason":    "mcp-servers",
						"updatedAt": time.Now().UnixMilli(),
					})
				}
			}
		}
	}()
}
