package mcp

import (
	"context"
	"time"
)

type ReconnectLoop struct {
	registry *Registry
	sync     *ToolSync
	gate     *AvailabilityGate
	interval time.Duration
}

func NewReconnectLoop(registry *Registry, sync *ToolSync, gate *AvailabilityGate, interval time.Duration) *ReconnectLoop {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &ReconnectLoop{registry: registry, sync: sync, gate: gate, interval: interval}
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
				_, _ = r.sync.RefreshServers(ctx, due)
			}
		}
	}()
}
