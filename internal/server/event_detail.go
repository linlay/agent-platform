package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/stream"
)

// Delivery preferences are resolved after admission/ownership validation, never
// from a request's unverified Agent identity or the currently selected client Agent.
func (s *Server) agentDefaultDetail(agentKey string) stream.DetailLevel {
	if s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(agentKey); ok && def.Interaction().DefaultDetail == "full" {
			return stream.DetailFull
		}
	}
	return stream.DetailSummary
}
func (s *Server) chatDisplay(agentKey string, override stream.DetailLevel) api.EventDisplay {
	defaultDetail := s.agentDefaultDetail(agentKey)
	effectiveDetail := override
	if effectiveDetail != stream.DetailFull && effectiveDetail != stream.DetailSummary {
		effectiveDetail = defaultDetail
	}
	return api.EventDisplay{DefaultDetail: defaultDetail, EffectiveDetail: effectiveDetail}
}
