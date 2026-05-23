package catalog

import (
	"fmt"
	"path/filepath"
	"strings"
)

const AgentModeCoder = "CODER"

var coderAgentProfile = agentModeProfile{
	Tools: []string{
		"bash",
		"file_read",
		"file_write",
		"file_edit",
		"file_grep",
		"datetime",
	},
	ContextTags: []string{"system", "session"},
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

type agentModeProfile struct {
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
	case AgentModeCoder:
		return "", fmt.Errorf("type: CODER is no longer supported; use mode: CODER")
	default:
		return "", fmt.Errorf("unsupported agent type %q", value)
	}
}

func parseAgentWorkspaceConfig(value any) AgentWorkspaceConfig {
	node := mapNode(value)
	root := strings.TrimSpace(stringNode(node["root"]))
	if root == "" {
		return AgentWorkspaceConfig{}
	}
	gitConfig := mapNode(node["git"])
	return AgentWorkspaceConfig{
		Root:               filepath.Clean(root),
		ProjectPromptFiles: listStrings(node["projectPromptFiles"]),
		Git: AgentWorkspaceGitConfig{
			ExpectedBranch: stringNode(gitConfig["expectedBranch"]),
		},
	}
}

func validateAgentModeWorkspace(mode string, workspace AgentWorkspaceConfig) error {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case AgentModeCoder:
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

func applyAgentModeProfileDefaults(def AgentDefinition) AgentDefinition {
	profile, ok := agentModeProfileFor(def.Mode)
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

func agentModeProfileFor(mode string) (agentModeProfile, bool) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case AgentModeCoder:
		return coderAgentProfile, true
	default:
		return agentModeProfile{}, false
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
