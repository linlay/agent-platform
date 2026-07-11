package server

import "context"

func (s *Server) reloadAgentCatalog(ctx context.Context) error {
	if s == nil {
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
	return nil
}
