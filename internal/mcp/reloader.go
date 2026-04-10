package mcp

import "context"

type RegistryReloader struct {
	registry *Registry
	sync     *ToolSync
}

func NewRegistryReloader(registry *Registry, sync *ToolSync) *RegistryReloader {
	return &RegistryReloader{registry: registry, sync: sync}
}

func (r *RegistryReloader) Reload(ctx context.Context) error {
	if r == nil || r.registry == nil {
		return nil
	}
	if err := r.registry.Reload(); err != nil {
		return err
	}
	if r.sync != nil {
		_, err := r.sync.Load(ctx)
		return err
	}
	return nil
}
