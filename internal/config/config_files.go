package config

import (
	"fmt"
	"strings"
)

func (c *Config) applyStructuredConfig() error {
	if err := c.applyRuntimeFile(ConfigFile("configs/runtime.yml")); err != nil {
		return err
	}
	if err := c.applyHostToolsFile(ConfigFile("configs/host-tools.yml")); err != nil {
		return err
	}
	c.applyPromptsFile(ConfigFile("configs/prompts.yml"))
	if err := c.applyCoderSettingsFile(ConfigFile("configs/coder-settings.yml")); err != nil {
		return err
	}
	if err := c.applyAIToolsFile(ConfigFile("configs/ai-tools.yml")); err != nil {
		return err
	}
	if err := c.applyChannelsFile(ConfigFile("configs/channels.yml")); err != nil {
		return err
	}
	return nil
}

func loadYAMLMap(path string) (map[string]any, error) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return nil, err
	}
	values, _ := tree.(map[string]any)
	return values, nil
}

func (c *Config) applyDesktopValues(values map[string]any) {
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
	fallback.RequestTimeout = intValue(anyValue(values["request-timeout"], fallback.RequestTimeout), fallback.RequestTimeout)
	return fallback
}

func (c *Config) applyContainerHubValues(path string, values map[string]any) error {
	if err := rejectDeprecatedYAMLKeys(path, "container-hub.request-timeout", values, "request-timeout-ms"); err != nil {
		return err
	}
	if err := rejectDeprecatedYAMLKeys(path, "container-hub.agent-idle-timeout", values, "agent-idle-timeout-ms"); err != nil {
		return err
	}
	if err := rejectDeprecatedYAMLKeys(path, "container-hub.destroy-queue-delay", values, "destroy-queue-delay-ms"); err != nil {
		return err
	}
	c.ContainerHub.BaseURL = stringValue(anyValue(values["base-url"], c.ContainerHub.BaseURL), c.ContainerHub.BaseURL)
	c.ContainerHub.AuthToken = stringValue(anyValue(values["auth-token"], c.ContainerHub.AuthToken), c.ContainerHub.AuthToken)
	c.ContainerHub.DefaultEnvironmentID = stringValue(anyValue(values["default-environment-id"], c.ContainerHub.DefaultEnvironmentID), c.ContainerHub.DefaultEnvironmentID)
	c.ContainerHub.RequestTimeout = intValue(anyValue(values["request-timeout"], c.ContainerHub.RequestTimeout), c.ContainerHub.RequestTimeout)
	c.ContainerHub.DefaultSandboxLevel = strings.ToLower(stringValue(anyValue(values["default-sandbox-level"], c.ContainerHub.DefaultSandboxLevel), c.ContainerHub.DefaultSandboxLevel))
	c.ContainerHub.AgentIdleTimeout = int64Value(anyValue(values["agent-idle-timeout"], c.ContainerHub.AgentIdleTimeout), c.ContainerHub.AgentIdleTimeout)
	c.ContainerHub.DestroyQueueDelay = int64Value(anyValue(values["destroy-queue-delay"], c.ContainerHub.DestroyQueueDelay), c.ContainerHub.DestroyQueueDelay)
	return nil
}

func (c *Config) applyRuntimeFile(path string) error {
	values, err := loadYAMLMap(path)
	if err != nil {
		return nil
	}
	if len(values) == 0 {
		return nil
	}
	if containerHub, ok := values["container-hub"].(map[string]any); ok && len(containerHub) > 0 {
		if err := c.applyContainerHubValues(path, containerHub); err != nil {
			return err
		}
	}
	if desktop, ok := values["desktop"].(map[string]any); ok && len(desktop) > 0 {
		if action, ok := desktop["action"].(map[string]any); ok && len(action) > 0 {
			if err := rejectDeprecatedYAMLKeys(path, "desktop.action.request-timeout", action, "request-timeout-ms"); err != nil {
				return err
			}
		}
		if cdp, ok := desktop["cdp"].(map[string]any); ok && len(cdp) > 0 {
			if err := rejectDeprecatedYAMLKeys(path, "desktop.cdp.request-timeout", cdp, "request-timeout-ms"); err != nil {
				return err
			}
		}
		c.applyDesktopValues(desktop)
	}
	if cors, ok := values["cors"].(map[string]any); ok && len(cors) > 0 {
		c.applyCORSValues(cors)
	}
	if billing, ok := values["billing"].(map[string]any); ok && len(billing) > 0 {
		c.applyBillingValues(billing)
	}
	if i18nValues, ok := values["i18n"].(map[string]any); ok && len(i18nValues) > 0 {
		c.I18N.DefaultLocale = stringValue(anyValue(i18nValues["defaultLocale"], c.I18N.DefaultLocale), c.I18N.DefaultLocale)
		c.I18N.DefaultLocale = stringValue(anyValue(i18nValues["default-locale"], c.I18N.DefaultLocale), c.I18N.DefaultLocale)
	}
	if budget, ok := values["budget"].(map[string]any); ok && len(budget) > 0 {
		if err := rejectDeprecatedBudgetKeys(path, "budget", budget); err != nil {
			return err
		}
		c.applyRuntimeBudgetValues(budget)
	}
	return nil
}

func (c *Config) applyBillingValues(values map[string]any) {
	c.Billing.Currency = strings.ToUpper(stringValue(anyValue(values["currency"], c.Billing.Currency), c.Billing.Currency))
}

func (c *Config) applyRuntimeBudgetValues(budget map[string]any) {
	c.Defaults.Budget.Timeout = intValue(anyValue(budget["timeout"], c.Defaults.Budget.Timeout), c.Defaults.Budget.Timeout)
	c.Defaults.Budget.Model = parseRetryBudgetConfig(budget["model"], c.Defaults.Budget.Model)
	c.Defaults.Budget.Tool = parseRetryBudgetConfig(budget["tool"], c.Defaults.Budget.Tool)
	hitl, _ := budget["hitl"].(map[string]any)
	if len(hitl) > 0 {
		c.Defaults.Budget.Hitl.Timeout = intValue(anyValue(hitl["timeout"], c.Defaults.Budget.Hitl.Timeout), c.Defaults.Budget.Hitl.Timeout)
		c.Defaults.Budget.Hitl.Question = parseHitlModeBudgetConfig(hitl["question"], c.Defaults.Budget.Hitl.Question)
		c.Defaults.Budget.Hitl.Approval = parseHitlModeBudgetConfig(hitl["approval"], c.Defaults.Budget.Hitl.Approval)
		c.Defaults.Budget.Hitl.Form = parseHitlModeBudgetConfig(hitl["form"], c.Defaults.Budget.Hitl.Form)
		c.Defaults.Budget.Hitl.Plan = parseHitlModeBudgetConfig(hitl["plan"], c.Defaults.Budget.Hitl.Plan)
	}
	c.Defaults.Budget.Stages = parseStageBudgetConfigs(budget["stages"], c.Defaults.Budget.Stages)
}

func parseRetryBudgetConfig(raw any, fallback RetryBudgetConfig) RetryBudgetConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	fallback.MaxCalls = intValue(anyValue(values["maxCalls"], fallback.MaxCalls), fallback.MaxCalls)
	fallback.Timeout = intValue(anyValue(values["timeout"], fallback.Timeout), fallback.Timeout)
	fallback.RetryCount = intValue(anyValue(values["retryCount"], fallback.RetryCount), fallback.RetryCount)
	return fallback
}

func parseStageBudgetConfigs(raw any, fallback map[string]StageBudgetConfig) map[string]StageBudgetConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	out := make(map[string]StageBudgetConfig, len(fallback)+len(values))
	for key, value := range fallback {
		out[strings.ToLower(strings.TrimSpace(key))] = value
	}
	for rawKey, rawValue := range values {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if key == "" {
			continue
		}
		stageValues, _ := rawValue.(map[string]any)
		if len(stageValues) == 0 {
			continue
		}
		stage := out[key]
		stage.MaxSteps = intValue(anyValue(stageValues["maxSteps"], stage.MaxSteps), stage.MaxSteps)
		stage.Tool = parseRetryBudgetConfig(stageValues["tool"], stage.Tool)
		out[key] = stage
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseHitlModeBudgetConfig(raw any, fallback HitlModeBudgetConfig) HitlModeBudgetConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	fallback.Timeout = intValue(anyValue(values["timeout"], fallback.Timeout), fallback.Timeout)
	return fallback
}

func (c *Config) applyAccessPolicyValues(values map[string]any) {
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

func (c *Config) applyBashValues(values map[string]any) {
	c.Bash.WorkingDirectory = stringValue(anyValue(values["working-directory"], c.Bash.WorkingDirectory), c.Bash.WorkingDirectory)
	c.Bash.AllowedCommands = csvOrList(anyValue(values["allowed-commands"], c.Bash.AllowedCommands), c.Bash.AllowedCommands)
	c.Bash.ShellFeaturesEnabled = boolValue(anyValue(values["shell-features-enabled"], c.Bash.ShellFeaturesEnabled), c.Bash.ShellFeaturesEnabled)
	c.Bash.ShellExecutable = stringValue(anyValue(values["shell-executable"], c.Bash.ShellExecutable), c.Bash.ShellExecutable)
	c.Bash.ShellArgs = csvOrList(anyValue(values["shell-args"], c.Bash.ShellArgs), c.Bash.ShellArgs)
	c.Bash.ShellTimeout = intValue(anyValue(values["shell-timeout"], c.Bash.ShellTimeout), c.Bash.ShellTimeout)
	c.Bash.MaxCommandChars = intValue(anyValue(values["max-command-chars"], c.Bash.MaxCommandChars), c.Bash.MaxCommandChars)
}

func (c *Config) applySandboxBashValues(values map[string]any) {
	security, _ := values["security"].(map[string]any)
	if len(security) == 0 {
		return
	}
	overrides, _ := security["bashsec-overrides"].(map[string]any)
	if len(overrides) > 0 {
		c.SandboxBash.Security.BashsecOverrides.OutputRedirection = normalizeAccessPolicyApprovalAction(
			stringValue(anyValue(overrides["output-redirection"], c.SandboxBash.Security.BashsecOverrides.OutputRedirection), c.SandboxBash.Security.BashsecOverrides.OutputRedirection),
			c.SandboxBash.Security.BashsecOverrides.OutputRedirection,
		)
		c.SandboxBash.Security.BashsecOverrides.HeredocOutputRedirection = normalizeAccessPolicyApprovalAction(
			stringValue(anyValue(overrides["heredoc-output-redirection"], c.SandboxBash.Security.BashsecOverrides.HeredocOutputRedirection), c.SandboxBash.Security.BashsecOverrides.HeredocOutputRedirection),
			c.SandboxBash.Security.BashsecOverrides.HeredocOutputRedirection,
		)
	}
	c.SandboxBash.Security.AuditAutoApprovals = boolValue(anyValue(security["audit-auto-approvals"], c.SandboxBash.Security.AuditAutoApprovals), c.SandboxBash.Security.AuditAutoApprovals)
}

func (c *Config) applyFileToolsValues(path string, values map[string]any) error {
	c.FileTools.WorkingDirectory = stringValue(anyValue(values["working-directory"], c.FileTools.WorkingDirectory), c.FileTools.WorkingDirectory)
	c.FileTools.MaxReadBytes = intValue(anyValue(values["max-read-bytes"], c.FileTools.MaxReadBytes), c.FileTools.MaxReadBytes)
	c.FileTools.MaxWriteBytes = intValue(anyValue(values["max-write-bytes"], c.FileTools.MaxWriteBytes), c.FileTools.MaxWriteBytes)
	c.FileTools.MaxBatchOps = intValue(anyValue(values["max-batch-ops"], c.FileTools.MaxBatchOps), c.FileTools.MaxBatchOps)
	c.FileTools.RequireWriteApproval = boolValue(anyValue(values["require-write-approval"], c.FileTools.RequireWriteApproval), c.FileTools.RequireWriteApproval)
	c.FileTools.RequireReadBeforeWrite = boolValue(anyValue(values["require-read-before-write"], c.FileTools.RequireReadBeforeWrite), c.FileTools.RequireReadBeforeWrite)
	if raw, ok := values["read-before-write-scope"]; ok {
		scope := strings.ToLower(strings.TrimSpace(stringValue(raw, "")))
		switch scope {
		case "", "run":
			c.FileTools.ReadBeforeWriteScope = "run"
		case "chat":
			c.FileTools.ReadBeforeWriteScope = "chat"
		default:
			return fmt.Errorf("%s: invalid file-tools.read-before-write-scope %q; expected run or chat", path, scope)
		}
	}
	c.FileTools.Hooks = parseFileToolsHooksConfig(values["hooks"], c.FileTools.Hooks)
	return nil
}

func rejectDeprecatedYAMLKeys(path string, target string, values map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return fmt.Errorf("%s: %q has moved to %s", path, key, target)
		}
	}
	return nil
}

func rejectDeprecatedBudgetKeys(path string, fieldPath string, values map[string]any) error {
	if err := rejectDeprecatedYAMLKeys(path, fieldPath+".timeout", values, "runTimeoutMs", "timeoutMs"); err != nil {
		return err
	}
	for key, raw := range values {
		child, ok := raw.(map[string]any)
		if !ok || len(child) == 0 {
			continue
		}
		nextPath := fieldPath + "." + key
		if key == "stages" {
			for stageKey, stageRaw := range child {
				stage, ok := stageRaw.(map[string]any)
				if !ok || len(stage) == 0 {
					continue
				}
				if err := rejectDeprecatedBudgetKeys(path, nextPath+"."+stageKey, stage); err != nil {
					return err
				}
			}
			continue
		}
		if err := rejectDeprecatedBudgetKeys(path, nextPath, child); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) applyHostToolsFile(path string) error {
	values, err := loadYAMLMap(path)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	if accessPolicy, ok := values["access-policy"].(map[string]any); ok && len(accessPolicy) > 0 {
		c.applyAccessPolicyValues(accessPolicy)
	}
	if bash, ok := values["bash"].(map[string]any); ok && len(bash) > 0 {
		if err := rejectDeprecatedYAMLKeys(path, "configs/host-tools.yml > access-policy", bash, "allowed-paths", "path-checked-commands", "path-check-bypass-commands"); err != nil {
			return err
		}
		if err := rejectDeprecatedYAMLKeys(path, "bash.shell-timeout", bash, "shell-timeout-ms"); err != nil {
			return err
		}
		if err := rejectDeprecatedYAMLKeys(path, "budget.hitl.timeout", bash, "hitl-default-timeout-ms"); err != nil {
			return err
		}
		c.applyBashValues(bash)
	}
	if sandboxBash, ok := values["sandbox-bash"].(map[string]any); ok && len(sandboxBash) > 0 {
		c.applySandboxBashValues(sandboxBash)
	}
	if fileTools, ok := values["file-tools"].(map[string]any); ok && len(fileTools) > 0 {
		if err := rejectDeprecatedYAMLKeys(path, "configs/host-tools.yml > access-policy", fileTools, "allowed-read-paths", "allowed-write-paths"); err != nil {
			return err
		}
		if err := c.applyFileToolsValues(path, fileTools); err != nil {
			return err
		}
		// Check LSP diagnostics inside file-tools.hooks.after-file-change
		if hooks, ok := fileTools["hooks"].(map[string]any); ok && len(hooks) > 0 {
			if after, ok := hooks["after-file-change"].(map[string]any); ok && len(after) > 0 {
				if lspValues, ok := after["lsp-diagnostics"].(map[string]any); ok && len(lspValues) > 0 {
					if err := rejectDeprecatedYAMLKeys(path, "file-tools.hooks.after-file-change.lsp-diagnostics.timeout", lspValues, "timeout-ms"); err != nil {
						return err
					}
				}
			}
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
	cfg.Timeout = intValue(anyValue(lspValues["timeout"], cfg.Timeout), cfg.Timeout)
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
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaults.Timeout
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

func (c *Config) applyCORSValues(values map[string]any) {
	c.CORS.Enabled = boolValue(anyValue(values["enabled"], c.CORS.Enabled), c.CORS.Enabled)
	c.CORS.PathPattern = stringValue(anyValue(values["path-pattern"], c.CORS.PathPattern), c.CORS.PathPattern)
	c.CORS.AllowedOriginPatterns = csvOrList(anyValue(values["allowed-origin-patterns"], c.CORS.AllowedOriginPatterns), c.CORS.AllowedOriginPatterns)
	c.CORS.AllowedMethods = csvOrList(anyValue(values["allowed-methods"], c.CORS.AllowedMethods), c.CORS.AllowedMethods)
	c.CORS.AllowedHeaders = csvOrList(anyValue(values["allowed-headers"], c.CORS.AllowedHeaders), c.CORS.AllowedHeaders)
	c.CORS.ExposedHeaders = csvOrList(anyValue(values["exposed-headers"], c.CORS.ExposedHeaders), c.CORS.ExposedHeaders)
	c.CORS.AllowCredentials = boolValue(anyValue(values["allow-credentials"], c.CORS.AllowCredentials), c.CORS.AllowCredentials)
	c.CORS.MaxAgeSeconds = intValue(anyValue(values["max-age-seconds"], c.CORS.MaxAgeSeconds), c.CORS.MaxAgeSeconds)
}

func (c *Config) applyPromptsFile(path string) {
	values, err := loadYAMLMap(path)
	if err != nil {
		return
	}
	if len(values) == 0 {
		return
	}
	c.applyPromptsValues(values)
	if coder, ok := values["coder"].(map[string]any); ok && len(coder) > 0 {
		c.applyCoderPromptsValues(coder)
	}
	if memory, ok := values["memory"].(map[string]any); ok && len(memory) > 0 {
		c.applyMemoryPromptsValues(memory)
	}
}

func (c *Config) applyPromptsValues(values map[string]any) {
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

func (c *Config) applyCoderPromptsValues(values map[string]any) {
	c.CoderPrompts.SystemPrompt = stringValue(anyValue(values["system-prompt"], c.CoderPrompts.SystemPrompt), c.CoderPrompts.SystemPrompt)
	c.CoderPrompts.PlanningPrompt = stringValue(anyValue(values["planning-prompt"], c.CoderPrompts.PlanningPrompt), c.CoderPrompts.PlanningPrompt)
	c.CoderPrompts.SummarySystemPrompt = stringValue(anyValue(values["summary-system-prompt"], c.CoderPrompts.SummarySystemPrompt), c.CoderPrompts.SummarySystemPrompt)
	c.CoderPrompts.SummaryUserPromptTemplate = stringValue(anyValue(values["summary-user-prompt-template"], c.CoderPrompts.SummaryUserPromptTemplate), c.CoderPrompts.SummaryUserPromptTemplate)
}

func (c *Config) applyMemoryPromptsValues(values map[string]any) {
	c.MemoryPrompts.SystemPromptTemplate = stringValue(anyValue(values["system-prompt-template"], c.MemoryPrompts.SystemPromptTemplate), c.MemoryPrompts.SystemPromptTemplate)
	c.MemoryPrompts.UserPromptTemplate = stringValue(anyValue(values["user-prompt-template"], c.MemoryPrompts.UserPromptTemplate), c.MemoryPrompts.UserPromptTemplate)
}

func (c *Config) applyCoderSettingsFile(path string) error {
	values, err := loadYAMLMap(path)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	defaultAgent, _ := values["default-agent"].(map[string]any)
	if len(defaultAgent) > 0 {
		c.CoderSettings.DefaultAgent.ModelKey = stringValue(anyValue(defaultAgent["modelKey"], c.CoderSettings.DefaultAgent.ModelKey), c.CoderSettings.DefaultAgent.ModelKey)
		c.CoderSettings.DefaultAgent.ReasoningEffort = stringValue(anyValue(defaultAgent["reasoningEffort"], c.CoderSettings.DefaultAgent.ReasoningEffort), c.CoderSettings.DefaultAgent.ReasoningEffort)
	}
	acpProxies, err := parseCoderACPProxies(values["acp-proxies"], c.CoderSettings.ACPProxies)
	if err != nil {
		return err
	}
	c.CoderSettings.ACPProxies = acpProxies
	workspaceAgents, _ := values["workspace-agents"].(map[string]any)
	if len(workspaceAgents) == 0 {
		return nil
	}
	c.CoderSettings.WorkspaceAgents.Enabled = boolValue(anyValue(workspaceAgents["enabled"], c.CoderSettings.WorkspaceAgents.Enabled), c.CoderSettings.WorkspaceAgents.Enabled)
	c.CoderSettings.WorkspaceAgents.File = stringValue(anyValue(workspaceAgents["file"], c.CoderSettings.WorkspaceAgents.File), c.CoderSettings.WorkspaceAgents.File)
	return nil
}

func parseCoderACPProxies(raw any, fallback map[string]CoderACPProxyConfig) (map[string]CoderACPProxyConfig, error) {
	out := make(map[string]CoderACPProxyConfig, len(fallback))
	for key, value := range fallback {
		out[strings.TrimSpace(key)] = value
	}
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return out, nil
	}
	for rawID, rawValue := range values {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("coder-settings config: acp-proxies id must not be empty")
		}
		proxyValues, ok := rawValue.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("coder-settings config: acp-proxies.%s must be an object", id)
		}
		cfg := CoderACPProxyConfig{}
		if existing, ok := out[id]; ok {
			cfg = existing
		}
		cfg.BaseURL = stringValue(anyValue(proxyValues["base-url"], cfg.BaseURL), cfg.BaseURL)
		cfg.AuthToken = stringValue(anyValue(proxyValues["auth-token"], cfg.AuthToken), cfg.AuthToken)
		if err := rejectDeprecatedYAMLKeys("coder-settings config", "acp-proxies."+id+".timeout", proxyValues, "timeout-ms"); err != nil {
			return nil, err
		}
		cfg.Timeout = intValue(anyValue(proxyValues["timeout"], cfg.Timeout), cfg.Timeout)
		if cfg.Timeout <= 0 {
			cfg.Timeout = 300
		}
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return nil, fmt.Errorf("coder-settings config: acp-proxies.%s.base-url is required", id)
		}
		out[id] = cfg
	}
	return out, nil
}

func (c *Config) applyVisionRecognizeValues(values map[string]any) {
	c.VisionRecognize.Enabled = boolValue(anyValue(values["enabled"], c.VisionRecognize.Enabled), c.VisionRecognize.Enabled)
	c.VisionRecognize.DefaultProfile = stringValue(anyValue(values["default-profile"], c.VisionRecognize.DefaultProfile), c.VisionRecognize.DefaultProfile)
	profiles, _ := values["profiles"].(map[string]any)
	if len(profiles) == 0 {
		return
	}
	parsed := make(map[string]VisionRecognizeProfileConfig, len(profiles))
	for key, raw := range profiles {
		profileKey := strings.TrimSpace(key)
		if profileKey == "" {
			continue
		}
		base := VisionRecognizeProfileConfig{}
		if existing, ok := c.VisionRecognize.Profiles[profileKey]; ok {
			base = existing
		}
		parsed[profileKey] = parseVisionRecognizeProfileConfig(raw, base)
	}
	c.VisionRecognize.Profiles = parsed
}

func (c *Config) applyAIToolsFile(path string) error {
	values, err := loadYAMLMap(path)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	if visionRecognize, ok := values["vision-recognize"].(map[string]any); ok && len(visionRecognize) > 0 {
		if profiles, ok := visionRecognize["profiles"].(map[string]any); ok && len(profiles) > 0 {
			for profileKey, profileRaw := range profiles {
				if profileValues, ok := profileRaw.(map[string]any); ok && len(profileValues) > 0 {
					if err := rejectDeprecatedYAMLKeys(path, "vision-recognize.profiles."+profileKey+".timeout", profileValues, "timeout-ms"); err != nil {
						return err
					}
				}
			}
		}
		c.applyVisionRecognizeValues(visionRecognize)
	}
	return nil
}

func parseVisionRecognizeProfileConfig(raw any, fallback VisionRecognizeProfileConfig) VisionRecognizeProfileConfig {
	values, _ := raw.(map[string]any)
	if len(values) == 0 {
		return fallback
	}
	fallback.ModelKey = stringValue(anyValue(values["model-key"], fallback.ModelKey), fallback.ModelKey)
	fallback.Timeout = intValue(anyValue(values["timeout"], fallback.Timeout), fallback.Timeout)
	fallback.MaxImages = intValue(anyValue(values["max-images"], fallback.MaxImages), fallback.MaxImages)
	fallback.MaxImageBytes = intValue(anyValue(values["max-image-bytes"], fallback.MaxImageBytes), fallback.MaxImageBytes)
	fallback.OutputFormat = stringValue(anyValue(values["output-format"], fallback.OutputFormat), fallback.OutputFormat)
	fallback.SystemPrompt = stringValue(anyValue(values["system-prompt"], fallback.SystemPrompt), fallback.SystemPrompt)
	return fallback
}

func (c *Config) applyChannelsFile(path string) error {
	values, err := loadYAMLMap(path)
	if err != nil {
		return err
	}
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
		URL:              stringValue(anyValue(gatewayMap["url"], ""), ""),
		JwtToken:         stringValue(anyValue(gatewayMap["jwt-token"], ""), ""),
		BaseURL:          stringValue(anyValue(gatewayMap["base-url"], ""), ""),
		HandshakeTimeout: int64Value(anyValue(gatewayMap["handshake-timeout"], 0), 0),
		ReconnectMin:     int64Value(anyValue(gatewayMap["reconnect-min"], 0), 0),
		ReconnectMax:     int64Value(anyValue(gatewayMap["reconnect-max"], 0), 0),
	}
	if err := rejectDeprecatedYAMLKeys("channels "+channelID+" gateway", "handshake-timeout", gatewayMap, "handshake-timeout-ms"); err != nil {
		return ChannelConfig{}, err
	}
	if err := rejectDeprecatedYAMLKeys("channels "+channelID+" gateway", "reconnect-min", gatewayMap, "reconnect-min-ms"); err != nil {
		return ChannelConfig{}, err
	}
	if err := rejectDeprecatedYAMLKeys("channels "+channelID+" gateway", "reconnect-max", gatewayMap, "reconnect-max-ms"); err != nil {
		return ChannelConfig{}, err
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
