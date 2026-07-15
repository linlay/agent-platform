package server

import "context"

func (s *Server) reloadAgentCatalog(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.deps.CatalogReloader != nil {
		if err := s.deps.CatalogReloader.Reload(ctx, "agents"); err != nil {
			return err
		}
		// The runtime reloader observer schedules this in the assembled app. Keep
		// this call for alternate server embeddings; debounce makes it idempotent.
		if s.deps.AgentCardRefresh != nil {
			s.deps.AgentCardRefresh.ScheduleRefresh()
		}
		return nil
	}
	if s.deps.Registry != nil {
		if err := s.deps.Registry.Reload(ctx, "agents"); err != nil {
			return err
		}
	}
	if s.deps.KBase != nil {
		s.deps.KBase.ReconcileWatchers(ctx)
	}
	if s.deps.AgentCardRefresh != nil {
		s.deps.AgentCardRefresh.ScheduleRefresh()
	}
	return nil
}
