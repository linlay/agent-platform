package schedule

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Orchestrator struct {
	registry   *Registry
	dispatcher *Dispatcher

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewOrchestrator(registry *Registry, dispatcher *Dispatcher) *Orchestrator {
	return &Orchestrator{
		registry:   registry,
		dispatcher: dispatcher,
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
		interval, err := parseSpec(def.Spec)
		if err != nil {
			return err
		}
		definition := def
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					_ = o.dispatcher.Dispatch(runCtx, definition)
				}
			}
		}()
	}
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

func parseSpec(spec string) (time.Duration, error) {
	normalized := strings.TrimSpace(spec)
	switch normalized {
	case "@hourly":
		return time.Hour, nil
	case "@daily":
		return 24 * time.Hour, nil
	}
	if strings.HasPrefix(normalized, "@every ") {
		return time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(normalized, "@every ")))
	}
	return 0, fmt.Errorf("unsupported schedule spec %q", spec)
}
