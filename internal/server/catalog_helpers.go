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
func cloneIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
func normalizeQueryModelServiceTier(value string) (string, bool) {
	return agentbuiltin.CoderNormalizeServiceTier(value)
}
func serviceTierAllowedForACPModel(serviceTier string, modelKey string, options []api.CoderModelOption) bool {
	return agentbuiltin.CoderServiceTierAllowedForACPModel(serviceTier, modelKey, options)
}
