package server

import (
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/api"
)

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func normalizeQueryModelServiceTier(value string) (string, bool) {
	return agentbuiltin.CoderNormalizeServiceTier(value)
}
func serviceTierAllowedForACPModel(serviceTier string, modelKey string, options []api.CoderModelOption) bool {
	return agentbuiltin.CoderServiceTierAllowedForACPModel(serviceTier, modelKey, options)
}
