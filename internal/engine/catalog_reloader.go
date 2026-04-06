package engine

import (
	"context"
	"sync/atomic"
	"time"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

type RuntimeCatalogReloader struct {
	registry     catalog.Registry
	models       *ModelRegistry
	lastReloadNs atomic.Int64
}

func NewRuntimeCatalogReloader(registry catalog.Registry, models *ModelRegistry) *RuntimeCatalogReloader {
	return &RuntimeCatalogReloader{
		registry: registry,
		models:   models,
	}
}

func (r *RuntimeCatalogReloader) Reload(ctx context.Context, reason string) error {
	if r.registry != nil {
		if err := r.registry.Reload(ctx, reason); err != nil {
			return err
		}
	}
	if r.models != nil {
		if err := r.models.Reload(); err != nil {
			return err
		}
	}
	r.lastReloadNs.Store(time.Now().UnixNano())
	return nil
}

func StartBackgroundReloaders(ctx context.Context, cfg config.Config, reloader CatalogReloader) {
	startTicker := func(intervalMs int64, reason string) {
		if intervalMs <= 0 || reloader == nil {
			return
		}
		ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = reloader.Reload(ctx, reason)
				}
			}
		}()
	}

	startTicker(cfg.Agents.RefreshIntervalMs, "agents")
	startTicker(cfg.Teams.RefreshIntervalMs, "teams")
	startTicker(cfg.Skills.RefreshIntervalMs, "skills")
	startTicker(cfg.Models.RefreshIntervalMs, "models")
	startTicker(cfg.Providers.RefreshIntervalMs, "providers")
}
