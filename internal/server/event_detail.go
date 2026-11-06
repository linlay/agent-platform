package server

import (
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
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
	return stream.ResolveDetailLevel("")
}

// detailFilter starts the delivery filter of one stream or history read;
// tool definitions that declare simpleVisible keep their calls at simple.
func (s *Server) detailFilter(level stream.DetailLevel) *stream.DetailFilter {
	lookup := s.toolLookup()
	if lookup == nil {
		return stream.NewDetailFilter(level, nil)
	}
	return stream.NewDetailFilter(level, func(toolName string) bool {
		def, ok := lookup.Tool(toolName)
		return ok && contracts.AnyBoolNode(def.Meta["simpleVisible"])
	})
}

// rejectDetailOverride answers 400 when a Run-starting request names a
// detail level: a Run is delivered at its Agent's configured level.
func rejectDetailOverride(w http.ResponseWriter, r *http.Request) bool {
	if !r.URL.Query().Has("detail") {
		return false
	}
	writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "detail follows the Agent configuration and cannot be set by the request", map[string]any{"code": "detail_not_allowed"}))
	return true
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
