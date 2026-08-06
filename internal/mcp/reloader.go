package mcp

import (
	"context"
	"sync"
)

type RegistryReloader struct {
	registry *Registry
	sync     *ToolSync

	mu                sync.Mutex
	lastSyncedVersion int64
}

func NewRegistryReloader(registry *Registry, sync *ToolSync) *RegistryReloader {
	lastSyncedVersion := int64(0)
	if sync != nil {
		lastSyncedVersion = sync.SyncedRegistryVersion()
	}
	return &RegistryReloader{registry: registry, sync: sync, lastSyncedVersion: lastSyncedVersion}
}

func (r *RegistryReloader) Reload(ctx context.Context) error {
	if r == nil || r.registry == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.registry.Reload(); err != nil {
		return err
	}
	version := r.registry.Version()
	if version == r.lastSyncedVersion {
		return nil
	}
	if r.sync != nil {
		if r.sync.client != nil {
			r.sync.client.Reconcile()
		}
		_, err := r.sync.Load(ctx)
		if err != nil {
			return err
		}
	}
	r.lastSyncedVersion = version
	return nil
}
