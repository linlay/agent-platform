package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func (s *Server) applyProxyRoutingConfig(def *catalog.AgentDefinition) *statusError {
	if def == nil {
		return nil
	}
	if catalog.AgentIsChannelMode(def.Mode) {
		return s.applyChannelImportRoutingConfig(def)
	}
	return nil
}

func (s *Server) applyChannelImportRoutingConfig(def *catalog.AgentDefinition) *statusError {
	channelID := strings.TrimSpace(def.ChannelConfig.ChannelID)
	remoteAgentKey := strings.TrimSpace(def.ChannelConfig.RemoteAgentKey)
	if channelID == "" || remoteAgentKey == "" {
		return &statusError{
			Status:  http.StatusServiceUnavailable,
			Message: "mode CHANNEL requires channelConfig.channelId and channelConfig.remoteAgentKey",
		}
	}
	if s.deps.Channels == nil {
		return &statusError{Status: http.StatusServiceUnavailable, Message: "channel registry is not configured"}
	}
	channelDef, ok := s.deps.Channels.Lookup(channelID)
	if !ok {
		return &statusError{Status: http.StatusServiceUnavailable, Message: "channel " + channelID + " is not configured"}
	}
	if channelDef.Mode == config.ChannelModeServer {
		provider, ok := s.deps.Notifications.(ChannelConnectionProvider)
		if !ok || provider == nil {
			return &statusError{Status: http.StatusServiceUnavailable, Message: "channel " + channelID + " is not connected"}
		}
		if _, ok := provider.GatewayConnection(channelID); !ok {
			return &statusError{Status: http.StatusServiceUnavailable, Message: "channel " + channelID + " is not connected"}
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
		return &statusError{Status: http.StatusServiceUnavailable, Message: "channel " + channelID + " is not client mode"}
	}
	upstreamURL := strings.TrimSpace(channelDef.Endpoint.URL)
	if upstreamURL == "" {
		return &statusError{Status: http.StatusServiceUnavailable, Message: "channel " + channelID + " is missing endpoint.url"}
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
