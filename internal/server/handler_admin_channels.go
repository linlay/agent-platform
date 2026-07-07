package server

import (
	"net/http"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	channelpkg "agent-platform/internal/channel"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
)

const (
	adminChannelStatusConnected    = "connected"
	adminChannelStatusDisconnected = "disconnected"
	adminChannelStatusUnavailable  = "unavailable"
)

func (s *Server) handleAdminChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.listAdminChannels()))
}

func (s *Server) handleMonitorChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.listAdminChannels()))
}

func (s *Server) listAdminChannels() api.AdminChannelListResponse {
	if s == nil || s.deps.Channels == nil {
		return api.AdminChannelListResponse{Items: []api.AdminChannelSummary{}, Total: 0}
	}
	defs := s.deps.Channels.All()
	items := make([]api.AdminChannelSummary, 0, len(defs))
	agentSummaries, agentDefs := s.channelAgentCatalog()
	connectionProvider, _ := s.deps.Notifications.(ChannelConnectionSnapshotProvider)
	for _, def := range defs {
		if def == nil {
			continue
		}
		connections := []ws.MonitorConnection(nil)
		if connectionProvider != nil {
			connections = connectionProvider.GatewayConnections(def.ID)
		}
		connection := adminChannelConnectionSummary(connections)
		status, connected := s.adminChannelStatus(def.Mode, def.ID, connection.ActiveCount, connectionProvider != nil)
		connection.Connected = connected
		items = append(items, api.AdminChannelSummary{
			ID:         strings.TrimSpace(def.ID),
			Name:       strings.TrimSpace(def.Name),
			Type:       strings.TrimSpace(string(def.Type)),
			Mode:       strings.TrimSpace(string(def.Mode)),
			Transport:  strings.TrimSpace(def.Transport),
			Protocol:   strings.TrimSpace(def.Protocol),
			Status:     status,
			Connection: connection,
			Agents:     s.adminChannelAgentSummary(def, agentSummaries, agentDefs),
			Config:     adminChannelConfigSummary(def.Endpoint, def.Auth, def.Heartbeat, def.Reconnect, def.Gateway),
		})
	}
	return api.AdminChannelListResponse{Items: items, Total: len(items)}
}

func (s *Server) adminChannelStatus(mode config.ChannelMode, channelID string, activeCount int, hasConnectionProvider bool) (string, bool) {
	connected := activeCount > 0
	statusKnown := false
	switch mode {
	case config.ChannelModeClient:
		if s != nil && s.deps.ChannelStatus != nil {
			statusKnown = true
			connected = s.deps.ChannelStatus.Connected(channelID) || connected
		} else if hasConnectionProvider {
			statusKnown = true
		}
	case config.ChannelModeServer:
		if hasConnectionProvider {
			statusKnown = true
		}
	default:
		statusKnown = hasConnectionProvider || (s != nil && s.deps.ChannelStatus != nil)
	}
	if !statusKnown {
		return adminChannelStatusUnavailable, connected
	}
	if connected {
		return adminChannelStatusConnected, true
	}
	return adminChannelStatusDisconnected, false
}

func adminChannelConnectionSummary(connections []ws.MonitorConnection) api.AdminChannelConnectionSummary {
	summary := api.AdminChannelConnectionSummary{ActiveCount: len(connections)}
	if len(connections) == 0 {
		return summary
	}
	latest := connections[0]
	summary.LatestSessionID = strings.TrimSpace(latest.SessionID)
	summary.ConnectedAt = latest.ConnectedAt
	summary.LastSeenAt = firstPositiveInt64(latest.LastSeenAt, latest.LastMessageAt, latest.ClosedAt)
	return summary
}

func (s *Server) channelAgentCatalog() ([]api.AgentSummary, map[string]catalog.AgentDefinition) {
	if s == nil || s.deps.Registry == nil {
		return nil, map[string]catalog.AgentDefinition{}
	}
	summaries := s.deps.Registry.Agents("all")
	defs := make(map[string]catalog.AgentDefinition, len(summaries))
	for _, summary := range summaries {
		key := strings.TrimSpace(summary.Key)
		if key == "" {
			continue
		}
		if def, ok := s.deps.Registry.AgentDefinition(key); ok {
			defs[key] = def
		}
	}
	return summaries, defs
}

func (s *Server) adminChannelAgentSummary(def *channelpkg.Definition, summaries []api.AgentSummary, defs map[string]catalog.AgentDefinition) api.AdminChannelAgentSummary {
	channelID := strings.TrimSpace(def.ID)
	allowedKeys := s.adminChannelAllowedAgentKeys(def, summaries)
	imports := make([]api.AdminChannelAgentImport, 0)
	exports := make([]api.AdminChannelAgentExport, 0)
	summaryNames := agentSummaryNames(summaries)
	for _, agentDef := range defs {
		agentKey := strings.TrimSpace(agentDef.Key)
		if agentKey == "" {
			continue
		}
		name := firstNonBlank(strings.TrimSpace(agentDef.Name), summaryNames[agentKey])
		if catalog.AgentIsChannelMode(agentDef.Mode) {
			if strings.TrimSpace(agentDef.ChannelConfig.ChannelID) == channelID {
				imports = append(imports, api.AdminChannelAgentImport{
					AgentKey:       agentKey,
					Name:           name,
					RemoteAgentKey: strings.TrimSpace(agentDef.ChannelConfig.RemoteAgentKey),
				})
			}
			continue
		}
		for _, export := range agentDef.ChannelConfig.Exports {
			if strings.TrimSpace(export.ChannelID) != channelID {
				continue
			}
			exports = append(exports, api.AdminChannelAgentExport{
				AgentKey:         agentKey,
				Name:             name,
				ExternalAgentKey: catalog.EffectiveChannelExportExternalKey(agentKey, export),
				Allow: api.AdminChannelAllowFlags{
					Query:        export.Allow.Query,
					Submit:       export.Allow.Submit,
					Steer:        export.Allow.Steer,
					Interrupt:    export.Allow.Interrupt,
					FileTransfer: export.Allow.FileTransfer,
				},
			})
		}
	}
	sort.Slice(imports, func(i, j int) bool {
		if imports[i].AgentKey == imports[j].AgentKey {
			return imports[i].RemoteAgentKey < imports[j].RemoteAgentKey
		}
		return imports[i].AgentKey < imports[j].AgentKey
	})
	sort.Slice(exports, func(i, j int) bool {
		if exports[i].AgentKey == exports[j].AgentKey {
			return exports[i].ExternalAgentKey < exports[j].ExternalAgentKey
		}
		return exports[i].AgentKey < exports[j].AgentKey
	})
	return api.AdminChannelAgentSummary{
		AllowedAllAgents: def.AllAgents,
		AllowedCount:     len(allowedKeys),
		AllowedAgentKeys: allowedKeys,
		ImportCount:      len(imports),
		ExportCount:      len(exports),
		Imports:          imports,
		Exports:          exports,
	}
}

func (s *Server) adminChannelAllowedAgentKeys(def *channelpkg.Definition, summaries []api.AgentSummary) []string {
	if def == nil {
		return nil
	}
	keys := []string{}
	if def.AllAgents {
		for _, summary := range summaries {
			key := strings.TrimSpace(summary.Key)
			if key == "" {
				continue
			}
			keys = append(keys, key)
		}
	} else if s != nil && s.deps.Channels != nil {
		keys = s.deps.Channels.AllowedAgentKeys(def.ID)
	}
	sort.Strings(keys)
	return keys
}

func agentSummaryNames(summaries []api.AgentSummary) map[string]string {
	names := make(map[string]string, len(summaries))
	for _, summary := range summaries {
		key := strings.TrimSpace(summary.Key)
		if key == "" {
			continue
		}
		names[key] = strings.TrimSpace(summary.Name)
	}
	return names
}

func adminChannelConfigSummary(
	endpoint config.ChannelEndpointConfig,
	auth config.ChannelAuthConfig,
	heartbeat config.ChannelHeartbeatConfig,
	reconnect config.ChannelReconnectConfig,
	gateway config.ChannelGatewayConfig,
) api.AdminChannelConfigSummary {
	return api.AdminChannelConfigSummary{
		EndpointURL:                      firstNonBlank(endpoint.URL, gateway.URL),
		EndpointPath:                     strings.TrimSpace(endpoint.Path),
		AuthType:                         strings.TrimSpace(auth.Type),
		HeartbeatIntervalSeconds:         heartbeat.Interval,
		ReconnectHandshakeTimeoutSeconds: firstPositiveInt64(reconnect.HandshakeTimeout, gateway.HandshakeTimeout),
		ReconnectMinSeconds:              firstPositiveInt64(reconnect.Min, gateway.ReconnectMin),
		ReconnectMaxSeconds:              firstPositiveInt64(reconnect.Max, gateway.ReconnectMax),
	}
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
