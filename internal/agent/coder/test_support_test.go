package coder

import "agent-platform/internal/contracts"

func ModelConfigReasoningEffort(modelConfig map[string]any) string {
	reasoning := contracts.AnyMapNode(modelConfig["reasoning"])
	if enabled, ok := reasoning["enabled"].(bool); ok && !enabled {
		return "NONE"
	}
	return contracts.AnyStringNode(reasoning["effort"])
}
