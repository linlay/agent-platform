package reload

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runtimewatch "agent-platform/internal/watch"
)

type catalogWatchGroup struct {
	root    string
	entries []watchEntry
	watcher *runtimewatch.Watcher
}

func (g *catalogWatchGroup) hasReason(reason string) bool {
	for _, entry := range g.entries {
		if entry.reason == reason {
			return true
		}
	}
	return false
}

type catalogWatchCoordinator struct {
	entries []watchEntry
	groups  []*catalogWatchGroup
	centers []string
	// loaded is accessed only while RuntimeCatalogReloader.reloadMu is held.
	loaded  map[string]string
	mu      sync.Mutex
	pending map[string]struct{}
	wake    chan struct{}
}

func (c *catalogWatchCoordinator) enqueue(reason string) {
	c.mu.Lock()
	c.pending[reason] = struct{}{}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *catalogWatchCoordinator) run(ctx context.Context, reload func(context.Context, string) error) {
	var timer *time.Timer
	var tick <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			// A bounded coalescing window avoids starvation under continuous events.
			if tick == nil {
				timer = time.NewTimer(reloadDebounce)
				tick = timer.C
			}
		case <-tick:
			tick = nil
			c.mu.Lock()
			pending := c.pending
			c.pending = make(map[string]struct{})
			c.mu.Unlock()
			// Stable category order; do not promote unrelated changes to a full reload.
			for _, entry := range c.entries {
				if _, ok := pending[entry.reason]; !ok {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if err := reload(ctx, entry.reason); err != nil {
					log.Printf("[reload] %s reconciliation failed: %v", entry.reason, err)
				}
			}
		}
	}
}

// Overlapping configured roots share one backend to avoid duplicate handles.
// Normal deployments have one independent group per resource category.
func groupWatchEntries(entries []watchEntry) []*catalogWatchGroup {
	ordered := append([]watchEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].path) < len(ordered[j].path) })
	var groups []*catalogWatchGroup
	for _, entry := range ordered {
		if strings.TrimSpace(entry.path) == "" {
			continue
		}
		assigned := false
		for _, group := range groups {
			if pathWithin(group.root, entry.path) {
				group.entries = append(group.entries, entry)
				assigned = true
				break
			}
		}
		if !assigned {
			groups = append(groups, &catalogWatchGroup{root: entry.path, entries: []watchEntry{entry}})
		}
	}
	return groups
}

// StartBackgroundReloaders keeps directory events separate from publication.
// API reloads and all watcher groups share one serialized execution boundary.
func StartBackgroundReloaders(ctx context.Context, cfg config.Config, reloader contracts.CatalogReloader) {
	if reloader == nil {
		return
	}
	c := &catalogWatchCoordinator{
		entries: backgroundWatchEntries(cfg), centers: []string{cfg.Paths.SkillsCenterDir, cfg.Paths.EffectiveConnectorsCenterDir()},
		loaded: make(map[string]string), pending: make(map[string]struct{}), wake: make(chan struct{}, 1),
	}
	owner, managed := reloader.(*RuntimeCatalogReloader)
	if managed {
		owner.reloadMu.Lock()
		defer owner.reloadMu.Unlock()
		if owner.background != nil {
			return
		}
		owner.background = c
	}
	for _, group := range groupWatchEntries(c.entries) {
		watcher, err := runtimewatch.Start(ctx, runtimewatch.Spec{
			LogPrefix: "[reload:" + group.entries[0].reason + "]",
			Roots:     []runtimewatch.Root{{Path: group.root, Recursive: true, ShouldTraverse: func(path string) bool { return catalog.ShouldWatchRuntimeDir(filepath.Base(path)) }}},
			Ignore:    func(path string) bool { return shouldIgnoreBackgroundWatchPath(path, c.centers...) },
			OnEvent: func(event runtimewatch.Event) {
				for _, entry := range group.entries {
					if pathWithin(entry.path, event.Path) {
						c.enqueue(entry.reason)
					}
				}
			},
			OnResume: func() {
				for _, entry := range group.entries {
					c.enqueue(entry.reason)
				}
			},
			OnError: func(error) {
				for _, entry := range group.entries {
					c.enqueue(entry.reason)
				}
			},
		})
		if err != nil {
			if !errors.Is(err, runtimewatch.ErrNoWatchedRoots) {
				log.Printf("[reload] watch %s unavailable: %v", group.root, err)
			}
			continue
		}
		group.watcher = watcher
		c.groups = append(c.groups, group)
	}
	reconcile := reloader.Reload
	if managed {
		reconcile = owner.reconcileWatch
	}
	go c.run(ctx, reconcile)
}
