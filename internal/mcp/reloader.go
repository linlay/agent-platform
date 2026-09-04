package mcp

import (
	"context"
	"sync"
)

type SyncScheduler interface {
	ScheduleSync()
}

type RegistryReloader struct {
	registry  *Registry
	sync      *ToolSync
	scheduler SyncScheduler

	mu                 sync.Mutex
	lastAppliedVersion int64
}

func NewRegistryReloader(registry *Registry, syncer *ToolSync, schedulers ...SyncScheduler) *RegistryReloader {
	lastAppliedVersion := int64(0)
	if registry != nil {
		lastAppliedVersion = registry.Version()
	}
	var scheduler SyncScheduler
	if len(schedulers) > 0 {
		scheduler = schedulers[0]
	}
	return &RegistryReloader{registry: registry, sync: syncer, scheduler: scheduler, lastAppliedVersion: lastAppliedVersion}
}

func (r *RegistryReloader) Reload(_ context.Context) error {
	if r == nil || r.registry == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.registry.Reload(); err != nil {
		return err
	}
	version := r.registry.Version()
	if version == r.lastAppliedVersion {
		return nil
	}
	if r.sync != nil {
		r.sync.ReconcileRegistry()
	}
	r.lastAppliedVersion = version
	if r.scheduler != nil {
		r.scheduler.ScheduleSync()
	}
	return nil
}
