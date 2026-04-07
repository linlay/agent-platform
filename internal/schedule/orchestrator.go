package schedule

import (
	"context"
	"log"
	"sync"
	"time"
)

type Orchestrator struct {
	registry   *Registry
	dispatcher *Dispatcher

	mu            sync.Mutex
	registrations map[string]Registration
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

type Registration struct {
	Definition Definition
}

func NewOrchestrator(registry *Registry, dispatcher *Dispatcher) *Orchestrator {
	return &Orchestrator{
		registry:      registry,
		dispatcher:    dispatcher,
		registrations: map[string]Registration{},
	}
}

func (o *Orchestrator) Start(ctx context.Context) error {
	if o == nil || o.registry == nil {
		return nil
	}
	defs, err := o.registry.Load()
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	o.cancel = cancel

	for _, def := range defs {
		if !def.Enabled {
			continue
		}
		schedule, err := parseCronSchedule(def.Cron)
		if err != nil {
			log.Printf("[schedule] skip registration for %q: %v", def.ID, err)
			continue
		}
		next := schedule.Next(time.Now())
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

		o.mu.Lock()
		o.registrations[def.ID] = Registration{Definition: def}
		o.mu.Unlock()

		definition := def
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			for {
				nextRun := schedule.Next(time.Now())
				timer := time.NewTimer(time.Until(nextRun))
				select {
				case <-runCtx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return
				case <-timer.C:
					_ = o.dispatcher.Dispatch(runCtx, definition)
				}
			}
		}()
	}

	o.mu.Lock()
	count := len(o.registrations)
	o.mu.Unlock()
	log.Printf("[schedule] registry ready count=%d", count)
	return nil
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
