package engine

import (
	"context"
	"log"
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
	start := time.Now()
	if r.registry != nil {
		if err := r.registry.Reload(ctx, reason); err != nil {
			log.Printf("[reload] %s catalog reload failed: %v", reason, err)
			return err
		}
	}
	if r.models != nil {
		if err := r.models.Reload(); err != nil {
			log.Printf("[reload] %s models reload failed: %v", reason, err)
			return err
		}
	}
	r.lastReloadNs.Store(time.Now().UnixNano())
	log.Printf("[reload] %s reloaded in %s", reason, time.Since(start).Truncate(time.Millisecond))
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
					log.Printf("[reload] %s background reloader stopped", reason)
					return
				case <-ticker.C:
					if err := reloader.Reload(ctx, reason); err != nil {
						log.Printf("[reload] %s periodic reload failed: %v", reason, err)
					}
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
