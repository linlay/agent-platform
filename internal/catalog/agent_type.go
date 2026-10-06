package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	agentcoder "agent-platform/internal/agent/coder"
	agentkbase "agent-platform/internal/agent/kbase"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/deprecation"
	"agent-platform/internal/kbase"
)

const AgentModeCoder = agentcoder.Mode
const AgentModeKBase = agentkbase.Mode
const AgentModeProxy = "PROXY"
const AgentModeChannel = "CHANNEL"
const removedChatWorkspaceRoot = "@chat"

var defaultAgentVisibilityScopes = []string{"nav"}

// AgentModeGeneral is the canonical spelling of the general-purpose native
// agent type. The legacy spelling is supported only when reading historical
// chat/run data; YAML and API inputs must use GENERAL.
const AgentModeGeneral = "GENERAL"
const agentModeLegacyReact = "REACT"

const AgentEngineNative = "native"
const AgentEngineACP = "acp"

func NormalizeAgentModeForRuntime(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", agentModeLegacyReact:
		return AgentModeGeneral
	case "PLAN-EXECUTE":
		return "PLAN_EXECUTE"
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
}

func AgentModeForAPI(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PLAN_EXECUTE":
		return "PLAN-EXECUTE"
	case agentModeLegacyReact:
		return AgentModeGeneral
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
}

// AgentModeAliases returns every stored spelling that carries the same
// meaning as the given public mode. Stored chat rows are never rewritten, so
// filters must match the historical spelling as well.
func AgentModeAliases(mode string) []string {
	if strings.EqualFold(strings.TrimSpace(mode), AgentModeGeneral) || strings.EqualFold(strings.TrimSpace(mode), agentModeLegacyReact) {
		return []string{AgentModeGeneral, agentModeLegacyReact}
	}
	return []string{strings.TrimSpace(mode)}
}

// ParsePublicAgentMode accepts only the stable YAML/API spellings. Runtime-only
// modes (TEAM and ONESHOT) are deliberately absent from this boundary.
func ParsePublicAgentMode(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return AgentModeGeneral, nil
	}
	switch strings.ToUpper(raw) {
	case AgentModeGeneral:
		return AgentModeGeneral, nil
	case agentModeLegacyReact:
		return "", deprecation.New("mode REACT was removed from configuration and API inputs; use GENERAL")
	case AgentModeCoder, AgentModeKBase, AgentModeProxy, AgentModeChannel:
		return strings.ToUpper(raw), nil
	case "PLAN-EXECUTE":
		return "PLAN_EXECUTE", nil
	case "ACP-PROXY", "ACP_PROXY":
		return "", deprecation.New("mode %q was removed; use PROXY", raw)
	case "PLAN_EXECUTE":
		return "", deprecation.New("mode %q was removed; use PLAN-EXECUTE", raw)
	case "ONESHOT":
		return "", deprecation.New("mode ONESHOT is internal-only and cannot be configured; use GENERAL")
	case "TEAM":
		return "", fmt.Errorf("mode TEAM is internal and can only be configured through a Team directory")
	default:
		return "", fmt.Errorf("mode must be GENERAL, CODER, KBASE, PLAN-EXECUTE, PROXY, or CHANNEL")
	}
}

// ParseAgentEngine accepts the top-level engine field. An absent field means
// the native engine; it is never inferred from runtimeConfig.acpBridgeId.
func ParseAgentEngine(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", AgentEngineNative:
		return AgentEngineNative, nil
	case AgentEngineACP:
		return AgentEngineACP, nil
	default:
		return "", fmt.Errorf("engine must be native or acp")
	}
}

// ParseAgentModeAndEngine resolves the two top-level type fields together.
// engine: acp has no mode of its own and always runs as CODER, so mode may be
// omitted there; any other explicit mode is a contradiction.
func ParseAgentModeAndEngine(rawMode string, rawEngine string) (string, string, error) {
	engine, err := ParseAgentEngine(rawEngine)
	if err != nil {
		return "", "", err
	}
	if engine == AgentEngineACP && strings.TrimSpace(rawMode) == "" {
		return AgentModeCoder, engine, nil
	}
	mode, err := ParsePublicAgentMode(rawMode)
	if err != nil {
		return "", "", err
	}
	if engine == AgentEngineACP && mode != AgentModeCoder {
		return "", "", fmt.Errorf("engine: acp does not take a mode; remove mode or use CODER")
	}
	return mode, engine, nil
}

// DefinitionRuntimeMode resolves the runtime mode of a raw definition map
// without validating it. It only adds the engine: acp default to
// NormalizeAgentModeForRuntime so callers that branch before validation agree
// with ParseAgentModeAndEngine.
func DefinitionRuntimeMode(definition map[string]any) string {
	rawMode := stringNode(definition["mode"])
	if rawMode == "" && strings.EqualFold(stringNode(definition["engine"]), AgentEngineACP) {
		return AgentModeCoder
	}
	return NormalizeAgentModeForRuntime(rawMode)
}

func AgentIsProxyMode(mode string) bool {
	return strings.EqualFold(NormalizeAgentModeForRuntime(mode), AgentModeProxy)
}

func AgentIsChannelMode(mode string) bool {
	return strings.EqualFold(NormalizeAgentModeForRuntime(mode), AgentModeChannel)
}

func parseAgentWorkspaceRoot(value any) AgentWorkspaceConfig {
	root := strings.TrimSpace(stringNode(value))
	if root == "" {
		return AgentWorkspaceConfig{}
	}
	if root == "@root" {
		// Windows resolves the current volume root; Unix/macOS use the filesystem root.
		if runtime.GOOS == "windows" {
			return AgentWorkspaceConfig{Root: cleanWorkspaceRoot(`\`), HostRoot: true}
		}
		return AgentWorkspaceConfig{Root: cleanWorkspaceRoot("/"), HostRoot: true}
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
	if strings.EqualFold(root, removedChatWorkspaceRoot) {
		return fmt.Errorf("runtimeConfig.workspaceRoot no longer supports %q; configure a real workspace directory", removedChatWorkspaceRoot)
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("runtimeConfig.workspaceRoot must be an absolute path")
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("runtimeConfig.workspaceRoot must be an existing directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtimeConfig.workspaceRoot must be a directory: %s", root)
	}
	return nil
}

func validateAgentModeWorkspace(mode string, workspace AgentWorkspaceConfig, kbaseConfig kbase.Config, hasRuntimeSandbox bool) error {
	if kbaseConfig.Enabled && strings.TrimSpace(workspace.Root) == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required when KBASE is enabled")
	}
	if agentcoder.IsMode(mode) && strings.TrimSpace(workspace.Root) == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required for CODER agents")
	}
	if hasRuntimeSandbox && strings.TrimSpace(workspace.Root) == "" {
		return fmt.Errorf("runtimeConfig.workspaceRoot is required for Container Hub sandbox agents")
	}
	return nil
}

func ValidateAgentCoderBackend(def AgentDefinition) error {
	acpBridgeID := strings.TrimSpace(def.ACPBridgeID)
	if def.Engine != AgentEngineACP {
		if acpBridgeID != "" {
			return fmt.Errorf("runtimeConfig.acpBridgeId requires engine: acp")
		}
		return nil
	}
	if acpBridgeID == "" {
		return fmt.Errorf("runtimeConfig.acpBridgeId is required for engine: acp")
	}
	if def.ProxyConfig != nil {
		return fmt.Errorf("proxyConfig is not supported for engine: acp; configure configs/agent-settings.yml acp-bridges and runtimeConfig.acpBridgeId")
	}
	if len(def.Project.PromptFiles) > 0 {
		return fmt.Errorf("projectConfig.promptFiles is not supported for engine: acp")
	}
	if len(def.ExcludedTools) > 0 {
		return fmt.Errorf("toolConfig.excludeTools is not supported for engine: acp")
	}
	if len(def.Tools) > 0 {
		return fmt.Errorf("toolConfig.tools is not supported for engine: acp; ACP bridges do not execute platform tools")
	}
	if len(def.Connectors) > 0 {
		return fmt.Errorf("connectorConfig.connectors is not supported for engine: acp; ACP bridges do not execute platform tools")
	}
	return nil
}

func ValidateAgentModelConfig(def AgentDefinition) error {
	if strings.EqualFold(strings.TrimSpace(def.Mode), agentteam.Mode) {
		return fmt.Errorf("mode TEAM is internal and can only be configured through a Team directory")
	}
	if AgentUsesACPCoderBackend(def) || AgentIsProxyMode(def.Mode) || AgentIsChannelMode(def.Mode) {
		return nil
	}
	if strings.TrimSpace(def.ModelKey) == "" {
		return fmt.Errorf("modelConfig.modelKey is required")
	}
	return nil
}

// ValidateOrdinaryAgentTools rejects removed tool names and tools reserved for
// runtime-synthesized internal agents. Directory agents and API-managed agents
// share this guard.
func ValidateOrdinaryAgentTools(tools []string) error {
	removedRunTools := map[string]string{
		"agent_run_query":     "run_query",
		"agent_run_status":    "run_status",
		"agent_run_interrupt": "run_interrupt",
	}
	for _, tool := range tools {
		normalized := strings.ToLower(strings.TrimSpace(tool))
		if replacement, removed := removedRunTools[normalized]; removed {
			return fmt.Errorf("tool %s was removed; use %s", normalized, replacement)
		}
		if normalized == "platform_config" {
			return fmt.Errorf("tool platform_config was removed; mount builtin.platform-control")
		}
		if normalized == strings.ToLower(agentteam.ToolDelegate) {
			return fmt.Errorf("tool %s is internal and can only be used by an orchestrated Team coordinator", agentteam.ToolDelegate)
		}
	}
	return nil
}

func ValidateAgentChannelConfig(def AgentDefinition) error {
	cfg := def.ChannelConfig
	if AgentIsChannelMode(def.Mode) {
		if strings.TrimSpace(cfg.ChannelID) == "" {
			return fmt.Errorf("channelConfig.channelId is required for mode: CHANNEL")
		}
		if strings.TrimSpace(cfg.RemoteAgentKey) == "" {
			return fmt.Errorf("channelConfig.remoteAgentKey is required for mode: CHANNEL")
		}
		if len(cfg.Exports) > 0 {
			return fmt.Errorf("channelConfig.exports is not supported for mode: CHANNEL")
		}
		return nil
	}
	if strings.TrimSpace(cfg.RemoteAgentKey) != "" {
		return fmt.Errorf("channelConfig.remoteAgentKey is only supported for mode: CHANNEL")
	}
	if strings.TrimSpace(cfg.ChannelID) != "" {
		return fmt.Errorf("channelConfig.channelId is only supported for mode: CHANNEL")
	}
	for i, export := range cfg.Exports {
		if strings.TrimSpace(export.ChannelID) == "" {
			return fmt.Errorf("channelConfig.exports[%d].channelId is required", i)
		}
	}
	return nil
}

// EffectiveChannelExportExternalKey returns the callable external agent key for an export.
// When export.ExternalAgentKey is explicitly set (non-blank) it is used as-is;
// otherwise the local agent key serves as the default external key.
func EffectiveChannelExportExternalKey(localAgentKey string, export AgentChannelExport) string {
	if ext := strings.TrimSpace(export.ExternalAgentKey); ext != "" {
		return ext
	}
	return strings.TrimSpace(localAgentKey)
}

// AgentEngineForAPI reports the engine of agents that Platform executes
// itself. PROXY and CHANNEL agents forward the run and have no engine.
func AgentEngineForAPI(def AgentDefinition) string {
	if AgentIsProxyMode(def.Mode) || AgentIsChannelMode(def.Mode) {
		return ""
	}
	if def.Engine == AgentEngineACP {
		return AgentEngineACP
	}
	return AgentEngineNative
}

func AgentUsesACPCoderBackend(def AgentDefinition) bool {
	return agentcoder.IsACPBackend(def.Mode, def.ACPBridgeID)
}

func applyAgentModeProfileDefaults(def AgentDefinition) AgentDefinition {
	profile, ok := agentModeProfileFor(def.Mode, def.ACPBridgeID)
	if !ok {
		return def
	}
	if agentIconEmpty(def.Icon) && strings.TrimSpace(profile.IconName) != "" {
		def.Icon = map[string]any{"name": profile.IconName}
	}
	if len(def.Tools) == 0 && len(profile.ToolNames) > 0 {
		def.Tools = append([]string(nil), profile.ToolNames...)
	}
	if len(def.ContextTags) == 0 && len(profile.ContextTags) > 0 {
		def.ContextTags = normalizeContextTags(profile.ContextTags)
	}
	if def.Budget == nil && len(profile.Budget) > 0 {
		def.Budget = cloneAgentProfileMap(profile.Budget)
	}
	return def
}

func agentIconEmpty(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func agentModeProfileFor(mode string, acpBridgeID string) (agentcontract.ModeProfile, bool) {
	return agentbuiltin.ProfileForAgent(mode, acpBridgeID)
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
