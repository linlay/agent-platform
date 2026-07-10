package server

import (
	"net/http"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func isProxyRoutedAgent(def catalog.AgentDefinition) bool {
	return isProxyAgentMode(def.Mode) || catalog.AgentIsChannelMode(def.Mode) || catalog.AgentUsesACPCoderBackend(def)
}

func (s *Server) applyProxyRoutingConfig(def *catalog.AgentDefinition) *statusError {
	if def == nil {
		return nil
	}
	if catalog.AgentIsChannelMode(def.Mode) {
		return s.applyChannelImportRoutingConfig(def)
	}
	if !catalog.AgentUsesACPCoderBackend(*def) {
		return nil
	}
	bridgeID := strings.TrimSpace(def.ACPBridgeID)
	if bridgeID == "" {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "runtimeConfig.acpBridgeId is required for ACP CODER",
		}
	}
	bridge, ok := s.deps.Config.CoderSettings.ACPBridges[bridgeID]
	if !ok {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "ACP bridge " + `"` + bridgeID + `" is not configured in configs/coder-settings.yml acp-bridges`,
		}
	}
	baseURL := strings.TrimSpace(bridge.BaseURL)
	if baseURL == "" {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "ACP bridge " + `"` + bridgeID + `" is missing base-url in configs/coder-settings.yml acp-bridges`,
		}
	}
	timeoutMS := bridge.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 300000
	}
	def.ProxyConfig = &catalog.ProxyConfig{
		BaseURL:   baseURL,
		Transport: "ws",
		Token:     strings.TrimSpace(bridge.AuthToken),
		Timeout:   (timeoutMS + 999) / 1000,
		TimeoutMS: timeoutMS,
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
		upstreamURL = strings.TrimSpace(channelDef.Gateway.URL)
	}
	if upstreamURL == "" {
		return &statusError{status: http.StatusServiceUnavailable, message: "channel " + channelID + " is missing endpoint.url"}
	}
	def.ProxyConfig = &catalog.ProxyConfig{
		BaseURL:      strings.TrimRight(upstreamURL, "/"),
		WebSocketURL: upstreamURL,
		Transport:    "ws",
		Protocol:     config.ChannelProtocolPlatformWS,
		AgentKey:     remoteAgentKey,
		Token:        firstNonBlank(strings.TrimSpace(channelDef.Endpoint.Token), strings.TrimSpace(channelDef.Gateway.JwtToken)),
		Timeout:      300,
	}
	return nil
}

func (s *Server) acpCoderModelOptions(session contracts.QuerySession, existing *api.QueryModelOptions) *api.QueryModelOptions {
	if !agentcoder.IsMode(session.Mode) {
		return existing
	}
	modelKey := strings.TrimSpace(session.ModelKey)
	reasoningEffort := ""
	serviceTier := ""
	if existing != nil {
		reasoningEffort = strings.TrimSpace(existing.ReasoningEffort)
		serviceTier = strings.TrimSpace(existing.ServiceTier)
		if strings.TrimSpace(existing.Key) != "" {
			modelKey = strings.TrimSpace(existing.Key)
		}
	}
	if modelKey == "" {
		if existing == nil || (reasoningEffort == "" && serviceTier == "") {
			return existing
		}
		return &api.QueryModelOptions{
			ReasoningEffort: reasoningEffort,
			ServiceTier:     serviceTier,
		}
	}
	return &api.QueryModelOptions{
		Key:             modelKey,
		ModelID:         s.modelIDForKey(modelKey),
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
	}
}

func (s *Server) modelIDForKey(modelKey string) string {
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return ""
	}
	if s.deps.Models != nil {
		if model, err := s.deps.Models.GetModel(modelKey); err == nil && strings.TrimSpace(model.ModelID) != "" {
			return strings.TrimSpace(model.ModelID)
		}
	}
	return modelKey
}
