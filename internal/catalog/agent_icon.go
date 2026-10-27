package catalog

import (
	"strings"

	"agent-platform/internal/contracts"
)

// Icon defaults are written only when creating an Agent. Its mode and graphic
// remain independent afterwards; explicit names, colors and image URLs survive.
func withAgentIconCreateDefault(definition map[string]any) map[string]any {
	out := contracts.CloneMap(definition)
	names := map[string]string{"CODER": "coder", "KBASE": "kbase", "TEAM": "team", "EXECUTOR": "executor", "PROXY": "proxy"}
	name := names[AgentModeForAPI(DefinitionRuntimeMode(out))]
	if name == "" {
		name = "general"
	}
	if icon, ok := out["icon"].(map[string]any); ok {
		if strings.TrimSpace(stringNode(icon["name"])) == "" {
			icon["name"] = name
		}
		return out
	}
	if out["icon"] == nil || strings.TrimSpace(stringNode(out["icon"])) == "" {
		out["icon"] = map[string]any{"name": name}
	}
	return out
}
