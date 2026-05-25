package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AgentModeCoder = "CODER"
const AgentWorkspaceRootChat = "@chat"

var defaultAgentVisibilityScopes = []string{"nav", "copilot", "invoke"}

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

func NormalizeAgentModeForRuntime(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "REACT"
	case "ACP-PROXY", "ACP_PROXY":
		return "PROXY"
	case "PLAN-EXECUTE", "PLAN_EXECUTE":
		return "PLAN_EXECUTE"
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
}

func AgentModeForAPI(value string) string {
	runtimeMode := NormalizeAgentModeForRuntime(value)
	switch runtimeMode {
	case "PLAN_EXECUTE":
		return "PLAN-EXECUTE"
	case "", "ONESHOT":
		return "REACT"
	default:
		return runtimeMode
	}
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

func parseAgentWorkspaceRoot(value any) AgentWorkspaceConfig {
	root := strings.TrimSpace(stringNode(value))
	if root == "" {
		return AgentWorkspaceConfig{}
	}
	return AgentWorkspaceConfig{Root: cleanWorkspaceRoot(root)}
}

func parseAgentVisibilityScopes(value any) []string {
	node := mapNode(value)
	scopes := listStrings(node["scopes"])
	if len(scopes) == 0 {
		return append([]string(nil), defaultAgentVisibilityScopes...)
	}
	out := make([]string, 0, len(scopes))
	seen := map[string]struct{}{}
	for _, raw := range scopes {
		scope := normalizeAgentVisibilityScope(raw)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	if len(out) == 0 {
		return append([]string(nil), defaultAgentVisibilityScopes...)
	}
	return out
}

func normalizeAgentVisibilityScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "nav", "copilot", "invoke", "internal":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func parseAgentKanbanConcurrency(value any) (int, error) {
	node := mapNode(value)
	if len(node) == 0 {
		return 1, nil
	}
	concurrency := intNode(node["concurrency"])
	if concurrency <= 0 {
		return 0, fmt.Errorf("kanban.concurrency must be greater than or equal to 1")
	}
	return concurrency, nil
}

func parseAgentProjectConfig(value any) AgentProjectConfig {
	node := mapNode(value)
	gitConfig := mapNode(node["git"])
	return AgentProjectConfig{
		PromptFiles: parseAgentProjectPromptFiles(node["promptFiles"]),
		Git: AgentProjectGitConfig{
			ExpectedBranch: stringNode(gitConfig["expectedBranch"]),
		},
	}
}

func parseAgentProjectPromptFiles(value any) []AgentProjectPromptFile {
	switch typed := value.(type) {
	case []any:
		out := make([]AgentProjectPromptFile, 0, len(typed))
		for _, item := range typed {
			if parsed, ok := parseAgentProjectPromptFile(item); ok {
				out = append(out, parsed)
			}
		}
		return out
	case []string:
		out := make([]AgentProjectPromptFile, 0, len(typed))
		for _, item := range typed {
			if parsed, ok := parseAgentProjectPromptFile(item); ok {
				out = append(out, parsed)
			}
		}
		return out
	case string:
		if parsed, ok := parseAgentProjectPromptFile(typed); ok {
			return []AgentProjectPromptFile{parsed}
		}
	}
	return nil
}

func parseAgentProjectPromptFile(value any) (AgentProjectPromptFile, bool) {
	if text := strings.TrimSpace(stringNode(value)); text != "" {
		const agentPrefix = "agent:"
		if strings.HasPrefix(text, agentPrefix) {
			return AgentProjectPromptFile{Source: "agent", Path: strings.TrimSpace(strings.TrimPrefix(text, agentPrefix))}, true
		}
		return AgentProjectPromptFile{Source: "workspace", Path: text}, true
	}
	node := mapNode(value)
	if len(node) == 0 {
		return AgentProjectPromptFile{}, false
	}
	path := strings.TrimSpace(stringNode(node["path"]))
	if path == "" {
		return AgentProjectPromptFile{}, false
	}
	source := normalizeProjectPromptSource(stringNode(node["source"]))
	if source == "" {
		source = "workspace"
	}
	return AgentProjectPromptFile{Source: source, Path: path}, true
}

func normalizeProjectPromptSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "workspace":
		return "workspace"
	case "agent", "agent-managed":
		return "agent"
	default:
		return strings.ToLower(strings.TrimSpace(source))
	}
}

func cleanWorkspaceRoot(root string) string {
	root = strings.TrimSpace(root)
	if strings.EqualFold(root, AgentWorkspaceRootChat) {
		return AgentWorkspaceRootChat
	}
	root = expandHomeWorkspaceRoot(root)
	return filepath.Clean(root)
}

func expandHomeWorkspaceRoot(root string) string {
	if root != "~" && !strings.HasPrefix(root, "~/") {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return root
	}
	if root == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(root, "~/"))
}

func validateAgentWorkspace(workspace AgentWorkspaceConfig) error {
	root := strings.TrimSpace(workspace.Root)
	if root == "" {
		return nil
	}
	if strings.EqualFold(root, AgentWorkspaceRootChat) {
		return nil
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("runtimeConfig.workspaceRoot must be an absolute path or %q", AgentWorkspaceRootChat)
	}
	return nil
}

func validateAgentModeWorkspace(mode string, workspace AgentWorkspaceConfig, hasRuntimeSandbox bool) error {
	if strings.ToUpper(strings.TrimSpace(mode)) != AgentModeCoder {
		return nil
	}
	if strings.TrimSpace(workspace.Root) == "" && !hasRuntimeSandbox {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required for non-sandbox CODER agents")
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
