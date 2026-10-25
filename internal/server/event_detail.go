package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/stream"
)

// Delivery preferences are resolved after admission/ownership validation, never
// from a request's unverified Agent identity or the currently selected client Agent.
func (s *Server) agentDefaultDetail(agentKey string) stream.DetailLevel {
	if s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(agentKey); ok {
			if level, valid := stream.ParseDetailLevel(def.Interaction().DefaultDetail); valid && level != "" {
				return level
			}
		}
	}
	return stream.DetailSummary
}
func (s *Server) chatDisplay(agentKey string, override stream.DetailLevel) api.EventDisplay {
	defaultDetail := s.agentDefaultDetail(agentKey)
	effectiveDetail := override
	if level, valid := stream.ParseDetailLevel(string(effectiveDetail)); !valid || level == "" {
		effectiveDetail = defaultDetail
	}
	return api.EventDisplay{DefaultDetail: defaultDetail, EffectiveDetail: effectiveDetail}
}
