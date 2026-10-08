package server

import (
	"context"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

// The published tools/skills remain the runtime catalog. Connector switches
// follow saved source even while a Run leases the previous runtime definition.
func (s *Server) buildAgentUsageResponse(ctx context.Context, def catalog.AgentDefinition) (api.AgentDetailResponse, error) {
	response := s.buildAgentDetailResponse(def)
	if _, ok := s.deps.Registry.(adminsource.AgentConnectorEditor); !ok {
		return response, nil
	}
	state, err := s.agentConnectorSelection(ctx, api.SetAgentConnectorRequest{AgentKey: def.Key}, false)
	if err != nil {
		return api.AgentDetailResponse{}, err
	}
	response.Connectors = agentConnectorUsageResponse(state).ConnectorIDs
	return response, nil
}
