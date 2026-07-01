package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
)

const AgentModeCoder = "CODER"
const AgentModeKBase = "KBASE"
const AgentWorkspaceRootChat = "@chat"
const DefaultCoderAgentIconName = agentcoder.DefaultIconName
const DefaultKBaseAgentIconName = "kbase"

var defaultAgentVisibilityScopes = []string{"nav"}

var kbaseAgentProfile = agentModeProfile{
	Tools: []string{
		"kbase_search",
		"kbase_files",
		"kbase_read",
		"kbase_status",
		"kbase_refresh",
		"datetime",
	},
	ContextTags: []string{"system", "session"},
	Budget: map[string]any{
		"timeout":  900,
		"maxSteps": 40,
		"tool": map[string]any{
			"maxCalls": 80,
		},
	},
}

type agentModeProfile struct {
	Tools       []string
	ContextTags []string
	Budget      map[string]any
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
	if root == "/" || root == "\\" {
		if abs, err := filepath.Abs(root); err == nil {
			return abs
		}
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
	if strings.EqualFold(strings.TrimSpace(mode), AgentModeKBase) {
		root := strings.TrimSpace(workspace.Root)
		if root == "" {
			return fmt.Errorf("runtimeConfig.workspaceRoot is required for mode: KBASE")
		}
		if strings.EqualFold(root, AgentWorkspaceRootChat) {
			return fmt.Errorf("runtimeConfig.workspaceRoot for mode: KBASE must be an absolute path or ~/ path, not %q", AgentWorkspaceRootChat)
		}
		if !filepath.IsAbs(root) {
			return fmt.Errorf("runtimeConfig.workspaceRoot for mode: KBASE must be an absolute path or ~/ path")
		}
	}
	return nil
}

func ValidateAgentCoderBackend(def AgentDefinition) error {
	acpProxyID := strings.TrimSpace(def.ACPProxyID)
	if acpProxyID != "" {
		if !agentcoder.IsMode(def.Mode) {
			return fmt.Errorf("runtimeConfig.acpProxyId is only supported for mode: CODER")
		}
		if acpProxyID == "" {
			return fmt.Errorf("runtimeConfig.acpProxyId is required for ACP CODER")
		}
		if def.ProxyConfig != nil {
			return fmt.Errorf("proxyConfig is not supported for ACP CODER; configure configs/coder-settings.yml acp-proxies and runtimeConfig.acpProxyId")
		}
		if len(def.Project.PromptFiles) > 0 {
			return fmt.Errorf("projectConfig.promptFiles is not supported for ACP CODER")
		}
		return nil
	}
	return nil
}

func ValidateAgentModelConfig(def AgentDefinition) error {
	if AgentUsesACPCoderBackend(def) {
		return nil
	}
	if strings.TrimSpace(def.ModelKey) == "" {
		return fmt.Errorf("modelConfig.modelKey is required")
	}
	return nil
}

func ValidateAgentKBaseConfig(def AgentDefinition) error {
	if !strings.EqualFold(strings.TrimSpace(def.Mode), AgentModeKBase) {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(def.KBaseConfig.Storage.Location)) {
	case "", "runtime", "workspace":
		return nil
	default:
		return fmt.Errorf("kbaseConfig.storage.location must be runtime or workspace")
	}
}

func AgentUsesACPCoderBackend(def AgentDefinition) bool {
	return agentcoder.IsACPBackend(def.Mode, def.ACPProxyID)
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
	return def
}

func agentModeProfileFor(mode string) (agentModeProfile, bool) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case AgentModeCoder:
		return agentModeProfile{
			Tools:       agentcoder.DefaultToolNames(),
			ContextTags: agentcoder.DefaultContextTags(),
			Budget:      agentcoder.DefaultBudget(),
		}, true
	case AgentModeKBase:
		return kbaseAgentProfile, true
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
