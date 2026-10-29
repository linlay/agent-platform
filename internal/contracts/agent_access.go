package contracts

import (
	"fmt"
)

// AgentAccessConfig is independent of mode and execution backend. An empty
// Default preserves the distinction between an omitted and declared default.
type AgentAccessConfig struct {
	Default string `json:"default,omitempty"`
}

func (c AgentAccessConfig) EffectiveDefault() string {
	if c.Default == "" || c.Default == AccessLevelDefault {
		return AccessLevelAskForApproval
	}
	return c.Default
}

// ParseAgentAccessConfig validates the source shape before applying defaults.
func ParseAgentAccessConfig(definition map[string]any) (AgentAccessConfig, error) {
	var c AgentAccessConfig
	raw, exists := definition["accessConfig"]
	if !exists {
		return c, nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return c, fmt.Errorf("accessConfig must be an object")
	}
	for key, value := range node {
		if key != "default" {
			return c, fmt.Errorf("unknown accessConfig field %q", key)
		}
		level, ok := value.(string)
		// Existing Agent files used default for the concrete approval level.
		if level == AccessLevelDefault {
			level = AccessLevelAskForApproval
		}
		if !ok || (level != AccessLevelAskForApproval && level != AccessLevelAutoApprove && level != AccessLevelFullAccess) {
			return c, fmt.Errorf("accessConfig.default must be default, ask_for_approval, auto_approve, or full_access")
		}
		c.Default = level
	}
	return c, nil
}

// ResolveInitialAccessLevel never inherits a caller's permission level.
func ResolveInitialAccessLevel(requested string, c AgentAccessConfig) (level, source string, err error) {
	if !IsDefaultAccessLevelRequest(requested) {
		level, ok := NormalizeAccessLevel(requested)
		if !ok {
			return "", "", fmt.Errorf("accessLevel must be default, ask_for_approval, auto_approve, or full_access")
		}
		return level, "explicit", nil
	}
	if c.Default != "" {
		return c.EffectiveDefault(), "agent_default", nil
	}
	return AccessLevelAskForApproval, "platform_default", nil
}

// ValidateExplicitRunAccess is checked again at the parent's atomic acceptance
// point. A target's configured default is intentionally not bounded by parent.
func ValidateExplicitRunAccess(requested, parent string) error {
	if !IsDefaultAccessLevelRequest(requested) && AccessLevelRank(requested) > AccessLevelRank(parent) {
		return &RunToolError{Code: "run_start_access_level_exceeded", Message: "explicit accessLevel exceeds the calling Run current level"}
	}
	return nil
}
