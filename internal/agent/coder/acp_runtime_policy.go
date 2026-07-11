package coder

import (
	"fmt"
	"strings"

	"agent-platform/internal/api"
)

const DefaultACPBridgeTimeoutMS = 300000

type ACPBridgeConfig struct {
	BaseURL   string
	AuthToken string
	TimeoutMS int
}

type ACPRoutingConfig struct {
	BaseURL   string
	Transport string
	Token     string
	Timeout   int
	TimeoutMS int
}

type ACPBridgeLookup func(bridgeID string) (ACPBridgeConfig, bool)

func ResolveACPBridge(bridgeID string, lookup ACPBridgeLookup) (ACPRoutingConfig, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return ACPRoutingConfig{}, fmt.Errorf("runtimeConfig.acpBridgeId is required for ACP CODER")
	}
	if lookup == nil {
		return ACPRoutingConfig{}, fmt.Errorf("ACP bridge %q is not configured in configs/coder-settings.yml acp-bridges", bridgeID)
	}
	bridge, ok := lookup(bridgeID)
	if !ok {
		return ACPRoutingConfig{}, fmt.Errorf("ACP bridge %q is not configured in configs/coder-settings.yml acp-bridges", bridgeID)
	}
	baseURL := strings.TrimSpace(bridge.BaseURL)
	if baseURL == "" {
		return ACPRoutingConfig{}, fmt.Errorf("ACP bridge %q is missing base-url in configs/coder-settings.yml acp-bridges", bridgeID)
	}
	timeoutMS := bridge.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = DefaultACPBridgeTimeoutMS
	}
	return ACPRoutingConfig{
		BaseURL:   baseURL,
		Transport: "ws",
		Token:     strings.TrimSpace(bridge.AuthToken),
		Timeout:   (timeoutMS + 999) / 1000,
		TimeoutMS: timeoutMS,
	}, nil
}

type ModelIDResolver func(modelKey string) string

func ResolveACPModelOptions(mode string, sessionModelKey string, existing *api.QueryModelOptions, resolveModelID ModelIDResolver) *api.QueryModelOptions {
	if !IsMode(mode) {
		return existing
	}
	modelKey := strings.TrimSpace(sessionModelKey)
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
	modelID := ""
	if resolveModelID != nil {
		modelID = strings.TrimSpace(resolveModelID(modelKey))
	}
	if modelID == "" {
		modelID = modelKey
	}
	return &api.QueryModelOptions{
		Key:             modelKey,
		ModelID:         modelID,
		ReasoningEffort: reasoningEffort,
		ServiceTier:     serviceTier,
	}
}
