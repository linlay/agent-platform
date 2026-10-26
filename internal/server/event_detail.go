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
	return stream.ResolveDetailLevel("", "")
}

// chatDisplay pairs the Agent default with the level actually delivered: a
// Run's frozen level, or the level a Chat with no active Run is read at.
func (s *Server) chatDisplay(agentKey string, override stream.DetailLevel) api.EventDisplay {
	defaultDetail := s.agentDefaultDetail(agentKey)
	effectiveDetail := override
	if level, valid := stream.ParseDetailLevel(string(effectiveDetail)); !valid || level == "" {
		effectiveDetail = defaultDetail
	}
	return api.EventDisplay{DefaultDetail: defaultDetail, EffectiveDetail: effectiveDetail}
}

// chatReadDetail is the level a Chat read is delivered at. While a Run is
// active every reader gets that Run's level and the requested one is
// ignored; a Chat with no active Run can be read at any level.
func chatReadDetail(response api.ChatDetailResponse, requested stream.DetailLevel) stream.DetailLevel {
	if response.ActiveRun != nil && response.ActiveRun.Detail != "" {
		return response.ActiveRun.Detail
	}
	return requested
}
