package reload

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	runtimewatch "agent-platform/internal/watch"
)

const reloadDebounce = 500 * time.Millisecond

// McpRegistryReloader is the minimal interface RuntimeCatalogReloader needs
// from the mcp package. Defined here to avoid an import cycle.
type McpRegistryReloader interface {
	Reload(ctx context.Context) error
}

// RuntimeToolReloader is implemented by the tool router so runtime tool YAML
// changes can take effect without rebuilding the app graph.
type RuntimeToolReloader interface {
	ReloadRuntimeToolDefinitions(root string) error
}

type RuntimeCatalogReloader struct {
	registry      catalog.Registry
	models        *models.ModelRegistry
	mcp           McpRegistryReloader
	tools         RuntimeToolReloader
	toolsDir      string
	notifications contracts.NotificationSink
	lastReloadNs  atomic.Int64
}

func NewRuntimeCatalogReloader(registry catalog.Registry, models *models.ModelRegistry, mcpRegistry McpRegistryReloader, tools RuntimeToolReloader, toolsDir string, notifications contracts.NotificationSink) *RuntimeCatalogReloader {
	return &RuntimeCatalogReloader{
		registry:      registry,
		models:        models,
		mcp:           mcpRegistry,
		tools:         tools,
		toolsDir:      toolsDir,
		notifications: notifications,
	}
}

// Reload dispatches reloads by reason. Reload spec:
//
//	agents          → reload agents (re-syncs declared skills from skills-market)
//	teams           → reload teams
//	skills          → reload skills + reload agents (cascade for synced skills)
//	models          → reload models + reload agents (cascade for affected agents)
//	providers       → reload providers only (independent)
//	tools           → reload runtime tool definitions + reload agents (cascade)
//	mcp-servers     → reload mcp registry + reload agents (cascade)
//	viewport-servers → reload agents (cascade; viewport server registry reads
//	                  on-demand and doesn't cache)
//	viewports       → broadcast update only (local viewports are read on-demand)
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
		if err := r.reloadCatalog(ctx, "skills"); err != nil {
			return err
		}
		log.Printf("[reload] cascade: skills → agents")
		if err := r.reloadCatalog(ctx, "agents"); err != nil {
			return err
		}
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
	case "tools":
		if r.tools != nil {
			if err := r.tools.ReloadRuntimeToolDefinitions(r.toolsDir); err != nil {
				log.Printf("[reload] tools reload failed: %v", err)
				return err
			}
		}
		log.Printf("[reload] cascade: tools → agents")
		if err := r.reloadCatalog(ctx, "agents"); err != nil {
			return err
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
	case "viewports":
		log.Printf("[reload] local viewports changed; registry reads templates on demand")
	default:
		// startup / config / unknown — full reload
		if r.tools != nil {
			if err := r.tools.ReloadRuntimeToolDefinitions(r.toolsDir); err != nil {
				log.Printf("[reload] %s tools reload failed: %v", reason, err)
				return err
			}
		}
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
	if r.notifications != nil {
		r.notifications.Broadcast("catalog.updated", map[string]any{
			"reason":    reason,
			"timestamp": time.Now().UnixMilli(),
		})
	}
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

	entries := backgroundWatchEntries(cfg)

	var pendingReason string
	var pendingPath string // last path printed in the current debounce window

	roots := make([]runtimewatch.Root, 0, len(entries))
	for _, entry := range entries {
		roots = append(roots, runtimewatch.Root{
			Path:      entry.path,
			Label:     entry.reason,
			Recursive: true,
			ShouldTraverse: func(path string) bool {
				return catalog.ShouldWatchRuntimeDir(filepath.Base(path))
			},
		})
	}

	_, err := runtimewatch.Start(ctx, runtimewatch.Spec{
		LogPrefix: "[reload]",
		Roots:     roots,
		Debounce:  reloadDebounce,
		Ignore: func(path string) bool {
			return catalog.ShouldIgnoreRuntimeWatchPath(path) || isAgentSkillsSubpath(path, cfg.Paths.AgentsDir)
		},
		OnEvent: func(event runtimewatch.Event) {
			reason := resolveChangeReason(event.Path, entries)
			// Dedupe: editors often emit multiple write events per save.
			// Only log once per (path, reason) within the debounce window.
			if pendingPath != event.Path || pendingReason != reason {
				log.Printf("[reload] change detected: %s (%s)", filepath.Base(event.Path), reason)
				pendingPath = event.Path
			}
			pendingReason = mergePendingReloadReason(pendingReason, reason)
		},
		OnDebounce: func(ctx context.Context) error {
			reloadReason := pendingReason
			pendingPath = ""
			pendingReason = ""
			if err := reloader.Reload(ctx, reloadReason); err != nil {
				return err
			}
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, runtimewatch.ErrNoWatchedRoots) {
			log.Printf("[reload] no directories to watch, file watching disabled")
			return
		}
		log.Printf("[reload] fsnotify init failed, file watching disabled: %v", err)
		return
	}
}

func mergePendingReloadReason(pending string, next string) string {
	pending = strings.TrimSpace(pending)
	next = strings.TrimSpace(next)
	if pending == "" {
		return next
	}
	if next == "" || pending == next {
		return pending
	}
	return "config"
}

func backgroundWatchEntries(cfg config.Config) []watchEntry {
	return []watchEntry{
		{cfg.Paths.AgentsDir, "agents"},
		{cfg.Paths.TeamsDir, "teams"},
		{cfg.Paths.SkillsMarketDir, "skills"},
		{filepath.Join(cfg.Paths.RegistriesDir, "models"), "models"},
		{filepath.Join(cfg.Paths.RegistriesDir, "providers"), "providers"},
		{cfg.Paths.ToolsDir, "tools"},
		{filepath.Join(filepath.Dir(filepath.Clean(cfg.Paths.RegistriesDir)), "viewports"), "viewports"},
		{filepath.Join(cfg.Paths.RegistriesDir, "mcp-servers"), "mcp-servers"},
		{filepath.Join(cfg.Paths.RegistriesDir, "viewport-servers"), "viewport-servers"},
	}
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
