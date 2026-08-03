package automation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	runtimewatch "agent-platform/internal/watch"

	"github.com/robfig/cron/v3"
)

const reloadDebounce = 150 * time.Millisecond

type Orchestrator struct {
	registry   *Registry
	dispatcher *Dispatcher
	cfg        config.AutomationConfig

	mu            sync.Mutex
	registrations map[string]*Registration
	cancel        context.CancelFunc
	runCtx        context.Context
	dispatchSlots chan struct{}
	wg            sync.WaitGroup
}

type Registration struct {
	Definition Definition
	automation cron.Schedule
	location   *time.Location
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewOrchestrator(registry *Registry, dispatcher *Dispatcher, cfg config.AutomationConfig) *Orchestrator {
	poolSize := cfg.PoolSize
	if poolSize < 1 {
		poolSize = 1
	}
	return &Orchestrator{
		registry:      registry,
		dispatcher:    dispatcher,
		cfg:           cfg,
		dispatchSlots: make(chan struct{}, poolSize),
		registrations: map[string]*Registration{},
	}
}

func (o *Orchestrator) Start(ctx context.Context) error {
	if o == nil || o.registry == nil {
		return nil
	}
	if err := os.MkdirAll(o.registry.root, 0o755); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.runCtx = runCtx
	o.cancel = cancel
	o.mu.Unlock()

	if err := o.Reload(); err != nil {
		cancel()
		return err
	}
	if err := o.startWatcher(runCtx); err != nil {
		cancel()
		return err
	}
	return nil
}

func (o *Orchestrator) Reload() error {
	if o == nil || o.registry == nil {
		return nil
	}
	defs, err := o.registry.Load()
	if err != nil {
		return err
	}
	o.reconcile(defs)
	return nil
}

func (o *Orchestrator) Automations() []AutomationInfo {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	ids := make([]string, 0, len(o.registrations))
	for id := range o.registrations {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	now := time.Now()
	items := make([]AutomationInfo, 0, len(ids))
	for _, id := range ids {
		reg := o.registrations[id]
		if reg == nil {
			continue
		}
		next := time.Time{}
		if reg.automation != nil {
			loc := reg.location
			if loc == nil {
				loc = time.Local
			}
			next = reg.automation.Next(now.In(loc))
		}
		items = append(items, AutomationInfo{
			Definition:   reg.Definition,
			NextFireTime: next,
		})
	}
	return items
}

func (o *Orchestrator) reconcile(defs []Definition) {
	desired := make(map[string]Definition, len(defs))
	for _, def := range defs {
		if !def.Enabled {
			continue
		}
		desired[def.ID] = def
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	for id := range o.registrations {
		if _, ok := desired[id]; ok {
			continue
		}
		o.unregisterLocked(id, "removed")
	}

	for _, def := range defs {
		if !def.Enabled {
			continue
		}
		reg, ok := o.registrations[def.ID]
		if ok && reflect.DeepEqual(reg.Definition, def) {
			continue
		}
		if ok {
			o.unregisterLocked(def.ID, "updated")
		}
		o.registerLocked(def)
	}

	log.Printf("[automation] registry ready count=%d", len(o.registrations))
}

func (o *Orchestrator) registerLocked(def Definition) {
	sched, err := parseCronAutomation(def.Cron)
	if err != nil {
		log.Printf("[automation] skip registration for %q: %v", def.ID, err)
		return
	}
	loc := resolveAutomationLocation(def.Environment.ZoneID, o.cfg.DefaultZoneID)
	regCtx, cancel := context.WithCancel(o.runCtx)
	reg := &Registration{
		Definition: def,
		automation: sched,
		location:   loc,
		ctx:        regCtx,
		cancel:     cancel,
	}
	o.registrations[def.ID] = reg

	next := sched.Next(time.Now().In(loc))
	log.Printf(
		"[automation] registered id=%s name=%s cron=%s agentKey=%s teamId=%s nextFireTime=%s source=%s",
		def.ID,
		def.Name,
		def.Cron,
		def.AgentKey,
		def.TeamID,
		next.Format(time.RFC3339),
		def.SourceFile,
	)

	o.wg.Add(1)
	go o.runRegistration(reg)
}

func (o *Orchestrator) unregisterLocked(id string, reason string) {
	reg, ok := o.registrations[id]
	if !ok {
		return
	}
	delete(o.registrations, id)
	reg.cancel()
	log.Printf("[automation] unregistered id=%s reason=%s source=%s", id, reason, reg.Definition.SourceFile)
}

func (o *Orchestrator) runRegistration(reg *Registration) {
	defer o.wg.Done()
	for {
		nextRun := reg.automation.Next(time.Now().In(reg.location))
		timer := time.NewTimer(time.Until(nextRun))
		select {
		case <-reg.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			stop, err := o.fire(reg)
			if err != nil {
				log.Printf("[automation] dispatch failed for %s: %v", reg.Definition.ID, err)
			}
			if stop {
				return
			}
		}
	}
}

func (o *Orchestrator) fire(reg *Registration) (bool, error) {
	o.mu.Lock()
	current, ok := o.registrations[reg.Definition.ID]
	if !ok || current != reg || reg.ctx.Err() != nil {
		o.mu.Unlock()
		return true, nil
	}

	dispatchDef := reg.Definition
	stop := false
	if reg.Definition.RemainingRuns != nil {
		nextRemaining := *reg.Definition.RemainingRuns - 1
		if nextRemaining > 0 {
			updated := reg.Definition
			updated.RemainingRuns = intPtr(nextRemaining)
			if err := o.registry.Persist(updated); err != nil {
				o.mu.Unlock()
				return false, err
			}
			reg.Definition = updated
			dispatchDef = updated
		} else {
			if err := o.registry.Delete(reg.Definition); err != nil {
				o.mu.Unlock()
				return false, err
			}
			delete(o.registrations, reg.Definition.ID)
			stop = true
			log.Printf("[automation] retired id=%s source=%s", reg.Definition.ID, reg.Definition.SourceFile)
		}
	}
	o.mu.Unlock()

	if o.dispatcher == nil {
		return stop, nil
	}
	if !o.acquireDispatchSlot(reg.ctx) {
		return true, reg.ctx.Err()
	}
	defer o.releaseDispatchSlot()
	zoneID := time.Local.String()
	if reg.location != nil {
		zoneID = reg.location.String()
	}
	return stop, o.dispatcher.Dispatch(reg.ctx, dispatchDef, zoneID)
}

func (o *Orchestrator) startWatcher(ctx context.Context) error {
	watcher, err := runtimewatch.Start(ctx, runtimewatch.Spec{
		LogPrefix: "[automation]",
		Roots: []runtimewatch.Root{{
			Path:           o.registry.root,
			Label:          "automations",
			Recursive:      true,
			ShouldTraverse: automationWatchShouldTraverse,
		}},
		Debounce: reloadDebounce,
		Ignore:   catalog.ShouldIgnoreRuntimeWatchPath,
		Include: func(event runtimewatch.Event) bool {
			return shouldReloadAutomationPath(o.registry.root, event.Path)
		},
		OnDebounce: func(context.Context) error {
			return o.Reload()
		},
	})
	if err != nil {
		if errors.Is(err, runtimewatch.ErrNoWatchedRoots) {
			log.Printf("[automation] no directories to watch, file watching disabled")
			return nil
		}
		return err
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		<-watcher.Done()
	}()
	return nil
}

func (o *Orchestrator) acquireDispatchSlot(ctx context.Context) bool {
	if o == nil || o.dispatchSlots == nil {
		return true
	}
	select {
	case o.dispatchSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (o *Orchestrator) releaseDispatchSlot() {
	if o == nil || o.dispatchSlots == nil {
		return
	}
	<-o.dispatchSlots
}

func automationWatchShouldTraverse(path string) bool {
	return shouldTraverseAutomationDir(filepath.Base(filepath.Clean(path)))
}

func shouldReloadAutomationPath(root string, path string) bool {
	path = filepath.Clean(path)
	if !insideDir(root, path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || rel == "." {
		return false
	}
	parts := splitPathParts(rel)
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts[:len(parts)-1] {
		if !shouldTraverseAutomationDir(part) {
			return false
		}
	}
	last := parts[len(parts)-1]
	if strings.EqualFold(filepath.Ext(last), ".tmp") {
		return false
	}
	if ext := strings.ToLower(filepath.Ext(last)); ext == ".yml" || ext == ".yaml" {
		return isAutomationRuntimeFile(path)
	}
	return shouldTraverseAutomationDir(last)
}

func splitPathParts(path string) []string {
	path = filepath.Clean(path)
	if path == "." || path == string(os.PathSeparator) || path == "" {
		return nil
	}
	return strings.Split(path, string(os.PathSeparator))
}

func resolveAutomationLocation(zoneID string, defaultZoneID string) *time.Location {
	if loc, err := loadAutomationLocation(zoneID); err == nil {
		return loc
	} else if strings.TrimSpace(zoneID) != "" {
		log.Printf("[automation] invalid zoneId %q, falling back: %v", zoneID, err)
	}
	if loc, err := loadAutomationLocation(defaultZoneID); err == nil {
		return loc
	} else if strings.TrimSpace(defaultZoneID) != "" {
		log.Printf("[automation] invalid default zoneId %q, using local: %v", defaultZoneID, err)
	}
	return time.Local
}

func loadAutomationLocation(zoneID string) (*time.Location, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return nil, fmt.Errorf("empty zoneId")
	}
	return time.LoadLocation(zoneID)
}

func (o *Orchestrator) Stop() context.Context {
	done, cancel := context.WithCancel(context.Background())
	go func() {
		if o != nil && o.cancel != nil {
			o.cancel()
		}
		if o != nil {
			o.wg.Wait()
		}
		cancel()
	}()
	return done
}

func intPtr(value int) *int {
	result := value
	return &result
}
