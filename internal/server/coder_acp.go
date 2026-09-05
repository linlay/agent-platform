package server

import (
	"net/http"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func isProxyRoutedAgent(def catalog.AgentDefinition) bool {
	return isProxyAgentMode(def.Mode) || catalog.AgentIsChannelMode(def.Mode) || agentbuiltin.IsCoderACPBackend(def.Mode, def.ACPBridgeID)
}

func (s *Server) applyProxyRoutingConfig(def *catalog.AgentDefinition) *statusError {
	if def == nil {
		return nil
	}
	if catalog.AgentIsChannelMode(def.Mode) {
		return s.applyChannelImportRoutingConfig(def)
	}
	if !agentbuiltin.IsCoderACPBackend(def.Mode, def.ACPBridgeID) {
		return nil
	}
	bridgeID := strings.TrimSpace(def.ACPBridgeID)
	routing, err := agentbuiltin.CoderResolveACPBridge(bridgeID, func(key string) (agentbuiltin.CoderACPBridgeConfig, bool) {
		bridge, ok := s.deps.Config.CoderSettings.ACPBridges[key]
		return agentbuiltin.CoderACPBridgeConfig{
			BaseURL:   bridge.BaseURL,
			AuthToken: bridge.AuthToken,
			TimeoutMS: bridge.TimeoutMS,
		}, ok
	})
	if err != nil {
		return &statusError{status: http.StatusServiceUnavailable, message: err.Error()}
	}
	def.ProxyConfig = &catalog.ProxyConfig{
		BaseURL:   routing.BaseURL,
		Transport: routing.Transport,
		Token:     routing.Token,
		Timeout:   routing.Timeout,
		TimeoutMS: routing.TimeoutMS,
	}
	return nil
}

func (s *Server) applyChannelImportRoutingConfig(def *catalog.AgentDefinition) *statusError {
	channelID := strings.TrimSpace(def.ChannelConfig.ChannelID)
	remoteAgentKey := strings.TrimSpace(def.ChannelConfig.RemoteAgentKey)
	if channelID == "" || remoteAgentKey == "" {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "mode CHANNEL requires channelConfig.channelId and channelConfig.remoteAgentKey",
		}
	}
	if s.deps.Channels == nil {
		return &statusError{status: http.StatusServiceUnavailable, message: "channel registry is not configured"}
	}
	channelDef, ok := s.deps.Channels.Lookup(channelID)
	if !ok {
		return &statusError{status: http.StatusServiceUnavailable, message: "channel " + channelID + " is not configured"}
	}
	if channelDef.Mode == config.ChannelModeServer {
		provider, ok := s.deps.Notifications.(ChannelConnectionProvider)
		if !ok || provider == nil {
			return &statusError{status: http.StatusServiceUnavailable, message: "channel " + channelID + " is not connected"}
		}
		if _, ok := provider.GatewayConnection(channelID); !ok {
			return &statusError{status: http.StatusServiceUnavailable, message: "channel " + channelID + " is not connected"}
		}
		def.ProxyConfig = &catalog.ProxyConfig{
			Transport: "ws",
			Protocol:  config.ChannelProtocolPlatformWS,
			AgentKey:  remoteAgentKey,
			ChannelID: channelID,
			Timeout:   300,
		}
		return nil
	}
	if channelDef.Mode != config.ChannelModeClient {
		return &statusError{status: http.StatusServiceUnavailable, message: "channel " + channelID + " is not client mode"}
	}
	upstreamURL := strings.TrimSpace(channelDef.Endpoint.URL)
	if upstreamURL == "" {
		return &statusError{status: http.StatusServiceUnavailable, message: "channel " + channelID + " is missing endpoint.url"}
	}
	def.ProxyConfig = &catalog.ProxyConfig{
		BaseURL:      strings.TrimRight(upstreamURL, "/"),
		WebSocketURL: upstreamURL,
		Transport:    "ws",
		Protocol:     config.ChannelProtocolPlatformWS,
		AgentKey:     remoteAgentKey,
		Token:        strings.TrimSpace(channelDef.Endpoint.Token),
		Timeout:      300,
	}
	return nil
}

func (s *Server) acpCoderModelOptions(session contracts.QuerySession, existing *api.QueryModelOptions) *api.QueryModelOptions {
	return agentbuiltin.CoderResolveACPModelOptions(session.Mode, session.ModelKey, existing, func(modelKey string) string {
		if s != nil && s.deps.Models != nil {
			if model, err := s.deps.Models.GetModel(modelKey); err == nil && strings.TrimSpace(model.ModelID) != "" {
				return strings.TrimSpace(model.ModelID)
			}
		}
		return ""
	})
}
