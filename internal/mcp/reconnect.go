package mcp

import (
	"context"
	"time"
)

type ReconnectLoop struct {
	registry *Registry
	client   *Client
	gate     *AvailabilityGate
	interval time.Duration
}

func NewReconnectLoop(registry *Registry, client *Client, gate *AvailabilityGate, interval time.Duration) *ReconnectLoop {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &ReconnectLoop{registry: registry, client: client, gate: gate, interval: interval}
}

func (r *ReconnectLoop) Start(ctx context.Context) {
	if r == nil || r.registry == nil || r.client == nil || r.gate == nil {
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
				for _, server := range r.registry.Servers() {
					if !r.gate.IsUnavailable(server.Key) {
						continue
					}
					_ = r.client.Initialize(ctx, server.Key)
				_, _ = r.client.ListTools(ctx, server.Key)
				}
			}
		}
	}()
}
