package reload

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
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/models"
)

const reloadDebounce = 500 * time.Millisecond

// McpRegistryReloader is the minimal interface RuntimeCatalogReloader needs
// from the mcp package. Defined here to avoid an import cycle.
type McpRegistryReloader interface {
	Reload(ctx context.Context) error
}

type HitlRegistryReloader interface {
	Reload() error
}

type RuntimeCatalogReloader struct {
	registry     catalog.Registry
	models       *models.ModelRegistry
	mcp          McpRegistryReloader
	hitl         HitlRegistryReloader
	lastReloadNs atomic.Int64
}

func NewRuntimeCatalogReloader(registry catalog.Registry, models *models.ModelRegistry, mcpRegistry McpRegistryReloader, hitlRegistry HitlRegistryReloader) *RuntimeCatalogReloader {
	return &RuntimeCatalogReloader{
		registry: registry,
		models:   models,
		mcp:      mcpRegistry,
		hitl:     hitlRegistry,
	}
}

// Reload dispatches reloads by reason. Reload spec:
//
//	agents          → reload agents (re-syncs declared skills from skills-market)
//	teams           → reload teams
//	skills          → no-op (skills-market is read-only catalog)
//	models          → reload models + reload agents (cascade for affected agents)
//	providers       → reload providers only (independent)
//	mcp-servers     → reload mcp registry + reload agents (cascade)
//	viewport-servers → reload agents (cascade; viewport server registry reads
//	                  on-demand and doesn't cache)
//	default / config → full reload
func (r *RuntimeCatalogReloader) Reload(ctx context.Context, reason string) error {
	start := time.Now()

	switch reason {
	case "agents":
		if err := r.reloadCatalog(ctx, "agents"); err != nil {
			return err
		}
	case "teams":
		if err := r.reloadCatalog(ctx, "teams"); err != nil {
			return err
		}
	case "skills":
		// noop — skills-market changes do not trigger any reload
		log.Printf("[reload] skills change ignored (skills-market is read-only)")
		return nil
	case "models":
		if r.models != nil {
			if err := r.models.ReloadModels(); err != nil {
				log.Printf("[reload] models reload failed: %v", err)
				return err
			}
		}
		log.Printf("[reload] cascade: models → agents")
		if err := r.reloadCatalog(ctx, "agents"); err != nil {
			return err
		}
	case "providers":
		if r.models != nil {
			if err := r.models.ReloadProviders(); err != nil {
				log.Printf("[reload] providers reload failed: %v", err)
				return err
			}
		}
	case "mcp-servers":
		if r.mcp != nil {
			if err := r.mcp.Reload(ctx); err != nil {
				log.Printf("[reload] mcp registry reload failed: %v", err)
				return err
			}
		}
		log.Printf("[reload] cascade: mcp-servers → agents")
		if err := r.reloadCatalog(ctx, "agents"); err != nil {
			return err
		}
	case "viewport-servers":
		log.Printf("[reload] cascade: viewport-servers → agents")
		if err := r.reloadCatalog(ctx, "agents"); err != nil {
			return err
		}
	case "bash-hitl":
		if r.hitl != nil {
			if err := r.hitl.Reload(); err != nil {
				log.Printf("[reload] bash-hitl reload failed: %v", err)
				return err
			}
		}
	default:
		// startup / config / unknown — full reload
		if err := r.reloadCatalog(ctx, reason); err != nil {
			return err
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

func (r *RuntimeCatalogReloader) reloadCatalog(ctx context.Context, reason string) error {
	if r.registry == nil {
		return nil
	}
	if err := r.registry.Reload(ctx, reason); err != nil {
		log.Printf("[reload] %s catalog reload failed: %v", reason, err)
		return err
	}
	return nil
}

type watchEntry struct {
	path   string
	reason string
}

// StartBackgroundReloaders watches runtime directories for changes and
// triggers a reload when files are created, modified, or deleted.
func StartBackgroundReloaders(ctx context.Context, cfg config.Config, reloader contracts.CatalogReloader) {
	if reloader == nil {
		return
	}

	entries := []watchEntry{
		{cfg.Paths.AgentsDir, "agents"},
		{cfg.Paths.TeamsDir, "teams"},
		{cfg.Paths.SkillsMarketDir, "skills"},
		{filepath.Join(cfg.Paths.RegistriesDir, "models"), "models"},
		{filepath.Join(cfg.Paths.RegistriesDir, "providers"), "providers"},
		{filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers"), "mcp-servers"},
		{filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers"), "viewport-servers"},
		{filepath.Join(cfg.Paths.RegistriesDir, "bash-hitl"), "bash-hitl"},
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
		var pendingPath string // last path printed in the current debounce window

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

				// Ignore changes inside per-agent skills/ subdirs — those are
				// written by reconcileDeclaredSkills and would cause an infinite
				// reload loop. Pattern: {agentsDir}/{agentKey}/skills/...
				if isAgentSkillsSubpath(event.Name, cfg.Paths.AgentsDir) {
					continue
				}

				reason := resolveChangeReason(event.Name, entries)
				// Dedupe: editors often emit multiple write events per save.
				// Only log once per (path, reason) within the debounce window.
				if pendingPath != event.Name || pendingReason != reason {
					log.Printf("[reload] change detected: %s (%s)", filepath.Base(event.Name), reason)
					pendingPath = event.Name
				}
				pendingReason = reason
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(reloadDebounce, func() {
					if err := reloader.Reload(ctx, pendingReason); err != nil {
						log.Printf("[reload] %s reload failed: %v", pendingReason, err)
					}
					pendingPath = ""
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

// isAgentSkillsSubpath returns true if changedPath is inside
// {agentsDir}/{agentKey}/skills/... — those files are managed by
// reconcileDeclaredSkills and must not retrigger a reload.
func isAgentSkillsSubpath(changedPath, agentsDir string) bool {
	if agentsDir == "" {
		return false
	}
	rel, err := filepath.Rel(agentsDir, changedPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// {agentKey}/skills/... → at least 2 parts with parts[1] == "skills"
	return len(parts) >= 2 && parts[1] == "skills"
}

func resolveChangeReason(changedPath string, dirs []watchEntry) string {
	for _, entry := range dirs {
		if strings.HasPrefix(changedPath, entry.path) {
			return entry.reason
		}
	}
	return "config"
}
