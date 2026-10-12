package query

import "agent-platform/internal/runtime/controlscope"

func (s *Service) runControlScopes() controlscope.Store {
	return controlscope.NewStore(s.deps.Runs)
}
