package config

import (
	"fmt"
	"strings"
)

func (c *Config) applyStructuredConfig() error {
	c.applyContainerHubFile(ConfigFile("configs/container-hub.yml"))
	c.applyDesktopFile(ConfigFile("configs/desktop.yml"))
	if err := c.applyAccessPolicyFile(ConfigFile("configs/access-policy.yml")); err != nil {
		return err
	}
	if err := c.applyBashFile(ConfigFile("configs/bash.yml")); err != nil {
		return err
	}
	if err := c.applyFileToolsFile(ConfigFile("configs/file-tools.yml")); err != nil {
		return err
	}
	c.applyCORSFile(ConfigFile("configs/cors.yml"))
	c.applyPromptsFile(ConfigFile("configs/prompts.yml"))
	c.applyCoderPromptsFile(ConfigFile("configs/coder-prompts.yml"))
	c.applyMemoryPromptsFile(ConfigFile("configs/memory-prompts.yml"))
	c.applyCoderSettingsFile(ConfigFile("configs/coder-settings.yml"))
	if err := c.applyChannelsFile(ConfigFile("configs/channels.yml")); err != nil {
		return err
	}
	return nil
}

func (c *Config) applyDesktopFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	c.Desktop.Action = parseDesktopBridgeConfig(values["action"], c.Desktop.Action)
	c.Desktop.CDP = parseDesktopBridgeConfig(values["cdp"], c.Desktop.CDP)
}

func parseDesktopBridgeConfig(raw any, fallback DesktopBridgeConfig) DesktopBridgeConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	fallback.Host = stringValue(anyValue(values["host"], fallback.Host), fallback.Host)
	fallback.Port = intValue(anyValue(values["port"], fallback.Port), fallback.Port)
	fallback.Path = stringValue(anyValue(values["path"], fallback.Path), fallback.Path)
	fallback.RequestTimeoutMs = intValue(anyValue(values["request-timeout-ms"], fallback.RequestTimeoutMs), fallback.RequestTimeoutMs)
	return fallback
}

func (c *Config) applyContainerHubFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	c.ContainerHub.BaseURL = stringValue(anyValue(values["base-url"], c.ContainerHub.BaseURL), c.ContainerHub.BaseURL)
	c.ContainerHub.AuthToken = stringValue(anyValue(values["auth-token"], c.ContainerHub.AuthToken), c.ContainerHub.AuthToken)
	c.ContainerHub.DefaultEnvironmentID = stringValue(anyValue(values["default-environment-id"], c.ContainerHub.DefaultEnvironmentID), c.ContainerHub.DefaultEnvironmentID)
	c.ContainerHub.RequestTimeoutMs = intValue(anyValue(values["request-timeout-ms"], c.ContainerHub.RequestTimeoutMs), c.ContainerHub.RequestTimeoutMs)
	c.ContainerHub.DefaultSandboxLevel = strings.ToLower(stringValue(anyValue(values["default-sandbox-level"], c.ContainerHub.DefaultSandboxLevel), c.ContainerHub.DefaultSandboxLevel))
	c.ContainerHub.AgentIdleTimeoutMs = int64Value(anyValue(values["agent-idle-timeout-ms"], c.ContainerHub.AgentIdleTimeoutMs), c.ContainerHub.AgentIdleTimeoutMs)
	c.ContainerHub.DestroyQueueDelayMs = int64Value(anyValue(values["destroy-queue-delay-ms"], c.ContainerHub.DestroyQueueDelayMs), c.ContainerHub.DestroyQueueDelayMs)
}

func (c *Config) applyAccessPolicyFile(path string) error {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return err
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return nil
	}
	c.AccessPolicy.Version = intValue(anyValue(values["version"], c.AccessPolicy.Version), c.AccessPolicy.Version)
	c.AccessPolicy.WorkingDirectory = stringValue(anyValue(values["working-directory"], c.AccessPolicy.WorkingDirectory), c.AccessPolicy.WorkingDirectory)
	if levels, ok := values["levels"].(map[string]any); ok && len(levels) > 0 {
		parsed := make(map[string]AccessPolicyLevelConfig, len(levels))
		for name, raw := range levels {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			base := AccessPolicyLevelConfig{}
			if existing, ok := c.AccessPolicy.Levels[name]; ok {
				base = existing
			}
			parsed[name] = parseAccessPolicyLevelConfig(raw, base)
		}
		for name, level := range c.AccessPolicy.Levels {
			if _, ok := parsed[name]; !ok {
				parsed[name] = level
			}
		}
		c.AccessPolicy.Levels = parsed
	}
	return nil
}

func parseAccessPolicyLevelConfig(raw any, fallback AccessPolicyLevelConfig) AccessPolicyLevelConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	fallback.Inherit = strings.ToLower(strings.TrimSpace(stringValue(anyValue(values["inherit"], fallback.Inherit), fallback.Inherit)))
	fallback.ReadRoots = csvOrList(anyValue(values["read-roots"], fallback.ReadRoots), fallback.ReadRoots)
	fallback.WriteRoots = csvOrList(anyValue(values["write-roots"], fallback.WriteRoots), fallback.WriteRoots)
	fallback.ReadonlyRoots = csvOrList(anyValue(values["readonly-roots"], fallback.ReadonlyRoots), fallback.ReadonlyRoots)
	fallback.Approvals = parseAccessPolicyApprovals(values["approvals"], fallback.Approvals)
	return fallback
}

func parseAccessPolicyApprovals(raw any, fallback AccessPolicyApprovalConfig) AccessPolicyApprovalConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	fallback.ReadOutsideRoots = stringValue(anyValue(values["read-outside-roots"], fallback.ReadOutsideRoots), fallback.ReadOutsideRoots)
	fallback.WriteOutsideRoots = stringValue(anyValue(values["write-outside-roots"], fallback.WriteOutsideRoots), fallback.WriteOutsideRoots)
	fallback.BashComplexFilesystem = stringValue(anyValue(values["bash-complex-filesystem"], fallback.BashComplexFilesystem), fallback.BashComplexFilesystem)
	fallback.BashOpaqueCommand = stringValue(anyValue(values["bash-opaque-command"], fallback.BashOpaqueCommand), fallback.BashOpaqueCommand)
	fallback.BashWriteInWriteRoots = stringValue(anyValue(values["bash-write-in-write-roots"], fallback.BashWriteInWriteRoots), fallback.BashWriteInWriteRoots)
	return fallback
}

func (c *Config) applyBashFile(path string) error {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return err
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return nil
	}
	if err := rejectDeprecatedYAMLKeys(path, values, "allowed-paths", "path-checked-commands", "path-check-bypass-commands"); err != nil {
		return err
	}
	c.Bash.WorkingDirectory = stringValue(anyValue(values["working-directory"], c.Bash.WorkingDirectory), c.Bash.WorkingDirectory)
	c.Bash.AllowedCommands = csvOrList(anyValue(values["allowed-commands"], c.Bash.AllowedCommands), c.Bash.AllowedCommands)
	c.Bash.ShellFeaturesEnabled = boolValue(anyValue(values["shell-features-enabled"], c.Bash.ShellFeaturesEnabled), c.Bash.ShellFeaturesEnabled)
	c.Bash.ShellExecutable = stringValue(anyValue(values["shell-executable"], c.Bash.ShellExecutable), c.Bash.ShellExecutable)
	c.Bash.ShellArgs = csvOrList(anyValue(values["shell-args"], c.Bash.ShellArgs), c.Bash.ShellArgs)
	c.Bash.ShellTimeoutMs = intValue(anyValue(values["shell-timeout-ms"], c.Bash.ShellTimeoutMs), c.Bash.ShellTimeoutMs)
	c.Bash.MaxCommandChars = intValue(anyValue(values["max-command-chars"], c.Bash.MaxCommandChars), c.Bash.MaxCommandChars)
	c.BashHITL.DefaultTimeoutMs = intValue(anyValue(values["hitl-default-timeout-ms"], c.BashHITL.DefaultTimeoutMs), c.BashHITL.DefaultTimeoutMs)
	return nil
}

func (c *Config) applyFileToolsFile(path string) error {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return err
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return nil
	}
	if err := rejectDeprecatedYAMLKeys(path, values, "allowed-read-paths", "allowed-write-paths"); err != nil {
		return err
	}
	c.FileTools.WorkingDirectory = stringValue(anyValue(values["working-directory"], c.FileTools.WorkingDirectory), c.FileTools.WorkingDirectory)
	c.FileTools.MaxReadBytes = intValue(anyValue(values["max-read-bytes"], c.FileTools.MaxReadBytes), c.FileTools.MaxReadBytes)
	c.FileTools.MaxWriteBytes = intValue(anyValue(values["max-write-bytes"], c.FileTools.MaxWriteBytes), c.FileTools.MaxWriteBytes)
	c.FileTools.MaxBatchOps = intValue(anyValue(values["max-batch-ops"], c.FileTools.MaxBatchOps), c.FileTools.MaxBatchOps)
	c.FileTools.RequireWriteApproval = boolValue(anyValue(values["require-write-approval"], c.FileTools.RequireWriteApproval), c.FileTools.RequireWriteApproval)
	c.FileTools.RequireReadBeforeWrite = boolValue(anyValue(values["require-read-before-write"], c.FileTools.RequireReadBeforeWrite), c.FileTools.RequireReadBeforeWrite)
	c.FileTools.Hooks = parseFileToolsHooksConfig(values["hooks"], c.FileTools.Hooks)
	return nil
}

func rejectDeprecatedYAMLKeys(path string, values map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return fmt.Errorf("%s: %q has moved to configs/access-policy.yml", path, key)
		}
	}
	return nil
}

func parseFileToolsHooksConfig(raw any, fallback FileToolsHooksConfig) FileToolsHooksConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	after, _ := values["after-file-change"].(map[string]any)
	if len(after) == 0 {
		return fallback
	}
	lspValues, _ := after["lsp-diagnostics"].(map[string]any)
	if len(lspValues) == 0 {
		return fallback
	}
	cfg := fallback.AfterFileChange.LSPDiagnostics
	cfg.Enabled = boolValue(anyValue(lspValues["enabled"], cfg.Enabled), cfg.Enabled)
	cfg.TimeoutMs = intValue(anyValue(lspValues["timeout-ms"], cfg.TimeoutMs), cfg.TimeoutMs)
	cfg.Languages = csvOrList(anyValue(lspValues["languages"], cfg.Languages), cfg.Languages)
	cfg.Servers = parseLSPServerConfigs(lspValues["servers"], cfg.Servers)
	fallback.AfterFileChange.LSPDiagnostics = cfg
	return fallback
}

func parseLSPServerConfigs(raw any, fallback map[string]LSPServerConfig) map[string]LSPServerConfig {
	out := cloneLSPServerConfigs(fallback)
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return out
	}
	for key, rawValue := range values {
		languageID := strings.ToLower(strings.TrimSpace(key))
		if languageID == "" {
			continue
		}
		serverValues, _ := rawValue.(map[string]any)
		if len(serverValues) == 0 {
			continue
		}
		cfg := out[languageID]
		cfg.Command = stringValue(anyValue(serverValues["command"], cfg.Command), cfg.Command)
		cfg.Args = csvOrList(anyValue(serverValues["args"], cfg.Args), cfg.Args)
		out[languageID] = cfg
	}
	return out
}

func cloneLSPServerConfigs(src map[string]LSPServerConfig) map[string]LSPServerConfig {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]LSPServerConfig, len(src))
	for key, value := range src {
		value.Args = append([]string(nil), value.Args...)
		out[strings.ToLower(strings.TrimSpace(key))] = value
	}
	return out
}

func normalizeLSPDiagnosticsHookConfig(cfg LSPDiagnosticsHookConfig) LSPDiagnosticsHookConfig {
	defaults := defaultLSPDiagnosticsHookConfig()
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = defaults.TimeoutMs
	}
	if len(cfg.Languages) == 0 {
		cfg.Languages = append([]string(nil), defaults.Languages...)
	}
	cfg.Languages = normalizeLanguageIDs(cfg.Languages)
	if len(cfg.Servers) == 0 {
		cfg.Servers = cloneLSPServerConfigs(defaults.Servers)
	} else {
		cfg.Servers = cloneLSPServerConfigs(cfg.Servers)
	}
	return cfg
}

func normalizeLanguageIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		languageID := strings.ToLower(strings.TrimSpace(value))
		if languageID == "" {
			continue
		}
		if _, ok := seen[languageID]; ok {
			continue
		}
		seen[languageID] = struct{}{}
		out = append(out, languageID)
	}
	return out
}

func (c *Config) applyCORSFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	c.CORS.Enabled = boolValue(anyValue(values["enabled"], c.CORS.Enabled), c.CORS.Enabled)
	c.CORS.PathPattern = stringValue(anyValue(values["path-pattern"], c.CORS.PathPattern), c.CORS.PathPattern)
	c.CORS.AllowedOriginPatterns = listValue(anyValue(values["allowed-origin-patterns"], c.CORS.AllowedOriginPatterns), c.CORS.AllowedOriginPatterns)
	c.CORS.AllowedMethods = listValue(anyValue(values["allowed-methods"], c.CORS.AllowedMethods), c.CORS.AllowedMethods)
	c.CORS.AllowedHeaders = listValue(anyValue(values["allowed-headers"], c.CORS.AllowedHeaders), c.CORS.AllowedHeaders)
	c.CORS.ExposedHeaders = listValue(anyValue(values["exposed-headers"], c.CORS.ExposedHeaders), c.CORS.ExposedHeaders)
	c.CORS.AllowCredentials = boolValue(anyValue(values["allow-credentials"], c.CORS.AllowCredentials), c.CORS.AllowCredentials)
	c.CORS.MaxAgeSeconds = intValue(anyValue(values["max-age-seconds"], c.CORS.MaxAgeSeconds), c.CORS.MaxAgeSeconds)
}

func (c *Config) applyPromptsFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	skill, _ := values["skill"].(map[string]any)
	if len(skill) > 0 {
		c.Prompts.Skill.InstructionsPrompt = stringValue(anyValue(skill["instructions-prompt"], c.Prompts.Skill.InstructionsPrompt), c.Prompts.Skill.InstructionsPrompt)
		c.Prompts.Skill.CatalogHeader = stringValue(anyValue(skill["catalog-header"], c.Prompts.Skill.CatalogHeader), c.Prompts.Skill.CatalogHeader)
		c.Prompts.Skill.DisclosureHeader = stringValue(anyValue(skill["disclosure-header"], c.Prompts.Skill.DisclosureHeader), c.Prompts.Skill.DisclosureHeader)
		c.Prompts.Skill.InstructionsLabel = stringValue(anyValue(skill["instructions-label"], c.Prompts.Skill.InstructionsLabel), c.Prompts.Skill.InstructionsLabel)
	}
	toolAppendix, _ := values["tool-appendix"].(map[string]any)
	if len(toolAppendix) > 0 {
		c.Prompts.ToolAppendix.ToolDescriptionTitle = stringValue(anyValue(toolAppendix["tool-description-title"], c.Prompts.ToolAppendix.ToolDescriptionTitle), c.Prompts.ToolAppendix.ToolDescriptionTitle)
		c.Prompts.ToolAppendix.AfterCallHintTitle = stringValue(anyValue(toolAppendix["after-call-hint-title"], c.Prompts.ToolAppendix.AfterCallHintTitle), c.Prompts.ToolAppendix.AfterCallHintTitle)
	}
	planExecute, _ := values["plan-execute"].(map[string]any)
	if len(planExecute) > 0 {
		c.Prompts.PlanExecute.TaskExecutionPromptTemplate = stringValue(anyValue(planExecute["task-execution-prompt-template"], c.Prompts.PlanExecute.TaskExecutionPromptTemplate), c.Prompts.PlanExecute.TaskExecutionPromptTemplate)
		c.Prompts.PlanExecute.PlanUserPromptTemplate = stringValue(anyValue(planExecute["plan-user-prompt-template"], c.Prompts.PlanExecute.PlanUserPromptTemplate), c.Prompts.PlanExecute.PlanUserPromptTemplate)
		c.Prompts.PlanExecute.SummarySystemPrompt = stringValue(anyValue(planExecute["summary-system-prompt"], c.Prompts.PlanExecute.SummarySystemPrompt), c.Prompts.PlanExecute.SummarySystemPrompt)
		c.Prompts.PlanExecute.SummaryUserPromptTemplate = stringValue(anyValue(planExecute["summary-user-prompt-template"], c.Prompts.PlanExecute.SummaryUserPromptTemplate), c.Prompts.PlanExecute.SummaryUserPromptTemplate)
	}
}

func (c *Config) applyCoderPromptsFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	c.CoderPrompts.PlanningPrompt = stringValue(anyValue(values["planning-prompt"], c.CoderPrompts.PlanningPrompt), c.CoderPrompts.PlanningPrompt)
	c.CoderPrompts.SummarySystemPrompt = stringValue(anyValue(values["summary-system-prompt"], c.CoderPrompts.SummarySystemPrompt), c.CoderPrompts.SummarySystemPrompt)
	c.CoderPrompts.SummaryUserPromptTemplate = stringValue(anyValue(values["summary-user-prompt-template"], c.CoderPrompts.SummaryUserPromptTemplate), c.CoderPrompts.SummaryUserPromptTemplate)
}

func (c *Config) applyMemoryPromptsFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	c.MemoryPrompts.SystemPromptTemplate = stringValue(anyValue(values["system-prompt-template"], c.MemoryPrompts.SystemPromptTemplate), c.MemoryPrompts.SystemPromptTemplate)
	c.MemoryPrompts.UserPromptTemplate = stringValue(anyValue(values["user-prompt-template"], c.MemoryPrompts.UserPromptTemplate), c.MemoryPrompts.UserPromptTemplate)
}

func (c *Config) applyCoderSettingsFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	workspaceAgents, _ := values["workspace-agents"].(map[string]any)
	if len(workspaceAgents) == 0 {
		return
	}
	c.CoderSettings.WorkspaceAgents.Enabled = boolValue(anyValue(workspaceAgents["enabled"], c.CoderSettings.WorkspaceAgents.Enabled), c.CoderSettings.WorkspaceAgents.Enabled)
	c.CoderSettings.WorkspaceAgents.File = stringValue(anyValue(workspaceAgents["file"], c.CoderSettings.WorkspaceAgents.File), c.CoderSettings.WorkspaceAgents.File)
}

func (c *Config) applyChannelsFile(path string) error {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return err
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return nil
	}
	rawChannels, ok := values["channels"]
	if !ok {
		return nil
	}
	channelMap, ok := rawChannels.(map[string]any)
	if !ok {
		return fmt.Errorf("channels config: channels must be a map")
	}
	configs := make([]ChannelConfig, 0, len(channelMap))
	for rawID, rawValue := range channelMap {
		channelID := strings.TrimSpace(rawID)
		if channelID == "" {
			return fmt.Errorf("channels config: channel id must not be empty")
		}
		entry, ok := rawValue.(map[string]any)
		if !ok {
			return fmt.Errorf("channels config: channel %q must be an object", channelID)
		}
		channelCfg, err := parseChannelConfig(channelID, entry)
		if err != nil {
			return err
		}
		configs = append(configs, channelCfg)
	}
	c.Channels = configs
	return nil
}

func parseChannelConfig(channelID string, values map[string]any) (ChannelConfig, error) {
	cfg := ChannelConfig{
		ID:   channelID,
		Name: stringValue(anyValue(values["name"], channelID), channelID),
	}
	rawType := strings.ToLower(strings.TrimSpace(stringValue(anyValue(values["type"], ""), "")))
	switch ChannelType(rawType) {
	case ChannelTypeBridge, ChannelTypeGateway:
		cfg.Type = ChannelType(rawType)
	default:
		return ChannelConfig{}, fmt.Errorf("channels config: channel %q has invalid type %q", channelID, rawType)
	}
	cfg.DefaultAgent = stringValue(anyValue(values["default-agent"], ""), "")
	allAgents, agents, err := parseChannelAgents(values["agents"])
	if err != nil {
		return ChannelConfig{}, fmt.Errorf("channels config: channel %q agents: %w", channelID, err)
	}
	cfg.AllAgents = allAgents
	cfg.Agents = agents
	gatewayMap, ok := values["gateway"].(map[string]any)
	if !ok || len(gatewayMap) == 0 {
		return ChannelConfig{}, fmt.Errorf("channels config: channel %q gateway is required", channelID)
	}
	cfg.Gateway = ChannelGatewayConfig{
		URL:                stringValue(anyValue(gatewayMap["url"], ""), ""),
		JwtToken:           stringValue(anyValue(gatewayMap["jwt-token"], ""), ""),
		BaseURL:            stringValue(anyValue(gatewayMap["base-url"], ""), ""),
		HandshakeTimeoutMs: int64Value(anyValue(gatewayMap["handshake-timeout-ms"], 0), 0),
		ReconnectMinMs:     int64Value(anyValue(gatewayMap["reconnect-min-ms"], 0), 0),
		ReconnectMaxMs:     int64Value(anyValue(gatewayMap["reconnect-max-ms"], 0), 0),
	}
	return cfg, nil
}

func parseChannelAgents(value any) (bool, []string, error) {
	if value == nil {
		return true, nil, nil
	}
	switch typed := value.(type) {
	case string:
		typed = strings.TrimSpace(typed)
		if typed == "" || typed == "*" {
			return true, nil, nil
		}
		return false, []string{typed}, nil
	case []any:
		agents := make([]string, 0, len(typed))
		seen := map[string]struct{}{}
		for _, item := range typed {
			agentKey := strings.TrimSpace(stringValue(item, ""))
			if agentKey == "" {
				return false, nil, fmt.Errorf("agent key must not be empty")
			}
			if agentKey == "*" {
				return false, nil, fmt.Errorf(`"*" must be used as a scalar, not inside a list`)
			}
			if _, exists := seen[agentKey]; exists {
				continue
			}
			seen[agentKey] = struct{}{}
			agents = append(agents, agentKey)
		}
		return false, agents, nil
	default:
		return false, nil, fmt.Errorf("must be \"*\" or a list of agent keys")
	}
}
