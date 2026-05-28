package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

func isProxyRoutedAgent(def catalog.AgentDefinition) bool {
	return isProxyAgentMode(def.Mode) || catalog.AgentUsesACPCoderBackend(def)
}

func (s *Server) applyProxyRoutingConfig(def *catalog.AgentDefinition) *statusError {
	if def == nil || !catalog.AgentUsesACPCoderBackend(*def) {
		return nil
	}
	proxyID := strings.TrimSpace(def.ACPProxyID)
	if proxyID == "" {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "runtimeConfig.acpProxyId is required for CODER agents using runtimeConfig.coderBackend: acp",
		}
	}
	proxy, ok := s.deps.Config.CoderSettings.ACPProxies[proxyID]
	if !ok {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "ACP proxy " + `"` + proxyID + `" is not configured in configs/coder-settings.yml acp-proxies`,
		}
	}
	baseURL := strings.TrimSpace(proxy.BaseURL)
	if baseURL == "" {
		return &statusError{
			status:  http.StatusServiceUnavailable,
			message: "ACP proxy " + `"` + proxyID + `" is missing base-url in configs/coder-settings.yml acp-proxies`,
		}
	}
	timeoutMs := proxy.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 300000
	}
	def.ProxyConfig = &catalog.ProxyConfig{
		BaseURL:   baseURL,
		Transport: "ws",
		Token:     strings.TrimSpace(proxy.AuthToken),
		TimeoutMs: timeoutMs,
	}
	return nil
}

func (s *Server) acpCoderModelOptions(session contracts.QuerySession, existing *api.QueryModelOptions) *api.QueryModelOptions {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), catalog.AgentModeCoder) {
		return existing
	}
	modelKey := strings.TrimSpace(session.ModelKey)
	reasoningEffort := ""
	if existing != nil {
		reasoningEffort = strings.TrimSpace(existing.ReasoningEffort)
		if strings.TrimSpace(existing.Key) != "" {
			modelKey = strings.TrimSpace(existing.Key)
		}
	}
	if modelKey == "" {
		if existing == nil || reasoningEffort == "" {
			return existing
		}
		return &api.QueryModelOptions{ReasoningEffort: reasoningEffort}
	}
	return &api.QueryModelOptions{
		Key:             modelKey,
		ModelID:         s.modelIDForKey(modelKey),
		ReasoningEffort: reasoningEffort,
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
