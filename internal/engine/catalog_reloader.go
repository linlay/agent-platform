package engine

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

const reloadDebounce = 500 * time.Millisecond

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

	// Dispatch by reason: catalog (agents/teams/skills) and model registry are
	// independent. Only reload what actually changed.
	switch reason {
	case "models", "providers":
		if r.models != nil {
			if err := r.models.Reload(); err != nil {
				log.Printf("[reload] %s models reload failed: %v", reason, err)
				return err
			}
		}
	case "agents", "teams", "skills":
		if r.registry != nil {
			if err := r.registry.Reload(ctx, reason); err != nil {
				log.Printf("[reload] %s catalog reload failed: %v", reason, err)
				return err
			}
		}
	default:
		// Unknown / startup / config — full reload
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
	}

	r.lastReloadNs.Store(time.Now().UnixNano())
	log.Printf("[reload] %s reloaded in %s", reason, time.Since(start).Truncate(time.Millisecond))
	return nil
}

type watchEntry struct {
	path   string
	reason string
}

// StartBackgroundReloaders watches runtime directories for changes and
// triggers a reload when files are created, modified, or deleted.
func StartBackgroundReloaders(ctx context.Context, cfg config.Config, reloader CatalogReloader) {
	if reloader == nil {
		return
	}

	entries := []watchEntry{
		{cfg.Paths.AgentsDir, "agents"},
		{cfg.Paths.TeamsDir, "teams"},
		{cfg.Paths.SkillsMarketDir, "skills"},
		{filepath.Join(cfg.Paths.RegistriesDir, "models"), "models"},
		{filepath.Join(cfg.Paths.RegistriesDir, "providers"), "providers"},
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[reload] fsnotify init failed, file watching disabled: %v", err)
		return
	}

	watched := 0
	for _, entry := range entries {
		if err := addRecursive(fsw, entry.path); err != nil {
			log.Printf("[reload] skip watch %s (%s): %v", entry.path, entry.reason, err)
			continue
		}
		watched++
		log.Printf("[reload] watching: %s (%s)", entry.path, entry.reason)
	}
	if watched == 0 {
		_ = fsw.Close()
		log.Printf("[reload] no directories to watch, file watching disabled")
		return
	}

	go func() {
		defer func() {
			_ = fsw.Close()
			log.Printf("[reload] file watcher stopped")
		}()

		var timer *time.Timer
		var pendingReason string

		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return

			case event, ok := <-fsw.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
					continue
				}

				// When a new directory is created, start watching it too.
				if event.Op&fsnotify.Create != 0 {
					if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
						_ = fsw.Add(event.Name)
					}
				}

				reason := resolveChangeReason(event.Name, entries)
				log.Printf("[reload] change detected: %s (%s)", filepath.Base(event.Name), reason)
				pendingReason = reason
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(reloadDebounce, func() {
					if err := reloader.Reload(ctx, pendingReason); err != nil {
						log.Printf("[reload] %s reload failed: %v", pendingReason, err)
					}
				})

			case watchErr, ok := <-fsw.Errors:
				if !ok {
					return
				}
				log.Printf("[reload] watcher error: %v", watchErr)
			}
		}
	}()
}

// addRecursive adds a directory and all its immediate subdirectories to the watcher.
func addRecursive(fsw *fsnotify.Watcher, root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fsw.Add(root)
	}
	if err := fsw.Add(root); err != nil {
		return err
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if entry.IsDir() {
			sub := filepath.Join(root, entry.Name())
			_ = fsw.Add(sub)
		}
	}
	return nil
}

func resolveChangeReason(changedPath string, dirs []watchEntry) string {
	for _, entry := range dirs {
		if strings.HasPrefix(changedPath, entry.path) {
			return entry.reason
		}
	}
	return "config"
}
