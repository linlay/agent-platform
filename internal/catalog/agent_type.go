package catalog

import (
	"fmt"
	"path/filepath"
	"strings"
)

const AgentTypeCoder = "CODER"

var coderAgentProfile = agentTypeProfile{
	Tools: []string{
		"bash",
		"file_read",
		"file_write",
		"file_grep",
		"datetime",
		"ask_user_question",
		"desktop_cdp",
	},
	ContextTags: []string{"system", "session", "owner"},
	Budget: map[string]any{
		"runTimeoutMs": 3600000,
		"model": map[string]any{
			"maxCalls": 240,
		},
		"tool": map[string]any{
			"maxCalls": 300,
		},
	},
	ReactMaxSteps: 160,
}

type agentTypeProfile struct {
	Tools         []string
	ContextTags   []string
	Budget        map[string]any
	ReactMaxSteps int
}

func normalizeAgentType(value string) (string, error) {
	agentType := strings.ToUpper(strings.TrimSpace(value))
	if agentType == "" {
		return "", nil
	}
	switch agentType {
	case AgentTypeCoder:
		return agentType, nil
	default:
		return "", fmt.Errorf("unsupported agent type %q", value)
	}
}

func parseAgentWorkspaceConfig(value any) AgentWorkspaceConfig {
	root := strings.TrimSpace(stringNode(mapNode(value)["root"]))
	if root == "" {
		return AgentWorkspaceConfig{}
	}
	return AgentWorkspaceConfig{Root: filepath.Clean(root)}
}

func validateAgentTypeWorkspace(agentType string, workspace AgentWorkspaceConfig) error {
	switch agentType {
	case AgentTypeCoder:
		root := strings.TrimSpace(workspace.Root)
		if root == "" {
			return fmt.Errorf("workspaceConfig.root is required for CODER agents")
		}
		if !filepath.IsAbs(root) {
			return fmt.Errorf("workspaceConfig.root must be an absolute path for CODER agents")
		}
	}
	return nil
}

func applyAgentTypeProfileDefaults(def AgentDefinition) AgentDefinition {
	profile, ok := agentTypeProfileFor(def.Type)
	if !ok {
		return def
	}
	if len(def.Tools) == 0 && len(profile.Tools) > 0 {
		def.Tools = append([]string(nil), profile.Tools...)
	}
	if len(def.ContextTags) == 0 && len(profile.ContextTags) > 0 {
		def.ContextTags = normalizeContextTags(profile.ContextTags)
	}
	if def.Budget == nil && len(profile.Budget) > 0 {
		def.Budget = cloneAgentProfileMap(profile.Budget)
	}
	if def.ReactMaxSteps <= 0 && profile.ReactMaxSteps > 0 {
		def.ReactMaxSteps = profile.ReactMaxSteps
	}
	return def
}

func agentTypeProfileFor(agentType string) (agentTypeProfile, bool) {
	switch strings.ToUpper(strings.TrimSpace(agentType)) {
	case AgentTypeCoder:
		return coderAgentProfile, true
	default:
		return agentTypeProfile{}, false
	}
}

func cloneAgentProfileMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			dst[key] = cloneAgentProfileMap(nested)
			continue
		}
		dst[key] = value
	}
	return dst
}
