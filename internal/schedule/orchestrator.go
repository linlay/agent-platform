package schedule

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/robfig/cron/v3"
)

const reloadDebounce = 150 * time.Millisecond

type Orchestrator struct {
	registry   *Registry
	dispatcher *Dispatcher

	mu            sync.Mutex
	registrations map[string]*Registration
	cancel        context.CancelFunc
	runCtx        context.Context
	wg            sync.WaitGroup
}

type Registration struct {
	Definition Definition
	schedule   cron.Schedule
	location   *time.Location
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewOrchestrator(registry *Registry, dispatcher *Dispatcher) *Orchestrator {
	return &Orchestrator{
		registry:      registry,
		dispatcher:    dispatcher,
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

	log.Printf("[schedule] registry ready count=%d", len(o.registrations))
}

func (o *Orchestrator) registerLocked(def Definition) {
	sched, err := parseCronSchedule(def.Cron)
	if err != nil {
		log.Printf("[schedule] skip registration for %q: %v", def.ID, err)
		return
	}
	loc := resolveScheduleLocation(def.Environment.ZoneID)
	regCtx, cancel := context.WithCancel(o.runCtx)
	reg := &Registration{
		Definition: def,
		schedule:   sched,
		location:   loc,
		ctx:        regCtx,
		cancel:     cancel,
	}
	o.registrations[def.ID] = reg

	next := sched.Next(time.Now().In(loc))
	log.Printf(
		"[schedule] registered id=%s name=%s cron=%s agentKey=%s teamId=%s nextFireTime=%s source=%s",
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
	log.Printf("[schedule] unregistered id=%s reason=%s source=%s", id, reason, reg.Definition.SourceFile)
}

func (o *Orchestrator) runRegistration(reg *Registration) {
	defer o.wg.Done()
	for {
		nextRun := reg.schedule.Next(time.Now().In(reg.location))
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
				log.Printf("[schedule] dispatch failed for %s: %v", reg.Definition.ID, err)
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
			log.Printf("[schedule] retired id=%s source=%s", reg.Definition.ID, reg.Definition.SourceFile)
		}
	}
	o.mu.Unlock()

	if o.dispatcher == nil {
		return stop, nil
	}
	return stop, o.dispatcher.Dispatch(reg.ctx, dispatchDef)
}

func (o *Orchestrator) startWatcher(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := fsw.Add(o.registry.root); err != nil {
		_ = fsw.Close()
		return err
	}

	log.Printf("[schedule] watching: %s", o.registry.root)
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer func() {
			_ = fsw.Close()
			log.Printf("[schedule] file watcher stopped")
		}()

		var timer *time.Timer
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
				if !isScheduleRuntimeFile(event.Name) {
					continue
				}
				if timer != nil {
					timer.Stop()
				}
				changedPath := event.Name
				timer = time.AfterFunc(reloadDebounce, func() {
					if err := o.Reload(); err != nil {
						log.Printf("[schedule] reload failed after %s: %v", filepath.Base(changedPath), err)
					}
				})
			case err, ok := <-fsw.Errors:
				if !ok {
					return
				}
				log.Printf("[schedule] watcher error: %v", err)
			}
		}
	}()
	return nil
}

func isScheduleRuntimeFile(path string) bool {
	name := filepath.Base(path)
	if strings.HasSuffix(name, ".tmp") {
		return false
	}
	if !catalogShouldLoadRuntimeName(name) {
		return false
	}
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

func resolveScheduleLocation(zoneID string) *time.Location {
	if strings.TrimSpace(zoneID) == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(strings.TrimSpace(zoneID))
	if err != nil {
		log.Printf("[schedule] invalid zoneId %q, using local: %v", zoneID, err)
		return time.Local
	}
	return loc
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

func catalogShouldLoadRuntimeName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".example.yml") || strings.HasSuffix(name, ".example.yaml") {
		return false
	}
	return true
}
