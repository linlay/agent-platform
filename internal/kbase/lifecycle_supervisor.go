package kbase

import (
	"context"
	"log"
	"time"
)

type lifecycleResolver interface {
	Keys() []string
	HasRequired() bool
}

type lifecycleWatchers interface {
	Start(ctx context.Context)
	Reconcile(ctx context.Context)
	Stop()
}

type lifecycleRefresh interface {
	Refresh(ctx context.Context, agentKey string, options RefreshOptions) (RefreshResult, error)
	BeginClose()
	Wait(ctx context.Context) error
}

type lifecycleLance interface {
	SetLifecycleContext(ctx context.Context)
	Probe(ctx context.Context, required bool) (bool, LanceEngineState, error)
	Stop(ctx context.Context) error
}

type lifecycleAuditor interface {
	Audit() ([]OrphanStorage, error)
}

type lifecycleSupervisor struct {
	reconcileInterval time.Duration
	resolver          lifecycleResolver
	watchers          lifecycleWatchers
	refresh           lifecycleRefresh
	lance             lifecycleLance
	auditor           lifecycleAuditor
}

func newLifecycleSupervisor(interval time.Duration, resolver lifecycleResolver, watchers lifecycleWatchers, refresh lifecycleRefresh, lance lifecycleLance, auditor lifecycleAuditor) *lifecycleSupervisor {
	return &lifecycleSupervisor{reconcileInterval: interval, resolver: resolver, watchers: watchers, refresh: refresh, lance: lance, auditor: auditor}
}

func (s *lifecycleSupervisor) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.lance.SetLifecycleContext(ctx)
	s.watchers.Start(ctx)
	if orphans, err := s.auditor.Audit(); err != nil {
		log.Printf("[kbase] orphan storage audit failed: %v", err)
	} else {
		for _, orphan := range orphans {
			log.Printf("[kbase] orphan storage path=%s sizeBytes=%d lastUsedAt=%d possibleOwner=%s", orphan.Path, orphan.SizeBytes, orphan.LastUsedAt, orphan.PossibleOwner)
		}
	}
	for _, key := range s.resolver.Keys() {
		agentKey := key
		go func() {
			if _, err := s.refresh.Refresh(ctx, agentKey, RefreshOptions{Mode: "startup"}); err != nil {
				log.Printf("[kbase] startup refresh failed agent=%s: %v", agentKey, err)
			}
		}()
	}
	interval := s.reconcileInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.watchers.Reconcile(ctx)
				for _, key := range s.resolver.Keys() {
					agentKey := key
					go func() {
						if _, err := s.refresh.Refresh(ctx, agentKey, RefreshOptions{Mode: "reconcile"}); err != nil {
							log.Printf("[kbase] reconcile refresh failed agent=%s: %v", agentKey, err)
						}
					}()
				}
			}
		}
	}()
}

func (s *lifecycleSupervisor) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.refresh.BeginClose()
	s.watchers.Stop()
	if err := s.refresh.Wait(ctx); err != nil {
		_ = s.lance.Stop(context.Background())
		return err
	}
	return s.lance.Stop(ctx)
}

func (s *lifecycleSupervisor) ProbeSidecar(ctx context.Context) (bool, LanceEngineState, error) {
	if s == nil || len(s.resolver.Keys()) == 0 {
		return false, LanceEngineState{}, nil
	}
	return s.lance.Probe(ctx, s.resolver.HasRequired())
}
