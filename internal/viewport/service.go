package viewport

import (
	"context"

	"agent-platform/internal/contracts"
)

type Service struct {
	registry *Registry
	syncer   *Syncer
	fallback contracts.ViewportClient
}

func NewServiceWithServers(registry *Registry, syncer *Syncer, fallback contracts.ViewportClient) *Service {
	return &Service{registry: registry, syncer: syncer, fallback: fallback}
}

func (s *Service) Get(ctx context.Context, viewportKey string) (map[string]any, error) {
	if s.registry != nil {
		payload, ok, err := s.registry.Get(viewportKey)
		if err != nil {
			return nil, err
		}
		if ok {
			return payload, nil
		}
	}
	if s.syncer != nil {
		payload, ok, err := s.syncer.Get(ctx, viewportKey)
		if err != nil {
			return nil, err
		}
		if ok {
			return payload, nil
		}
	}
	if s.fallback != nil {
		return s.fallback.Get(ctx, viewportKey)
	}
	return map[string]any{"viewportKey": viewportKey, "status": "not_implemented"}, nil
}
