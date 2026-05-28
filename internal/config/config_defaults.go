package config

import (
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
)

func defaultConfig() Config {
	runtimeRoot := strings.TrimSpace(os.Getenv("RUNTIME_DIR"))
	if runtimeRoot == "" {
		runtimeRoot = strings.TrimSpace(os.Getenv("SERVICE_DATA_DIR"))
	}
	if runtimeRoot == "" {
		runtimeRoot = "runtime"
	}
	paths := PathsConfig{
		RegistriesDir:   filepath.Join(runtimeRoot, "registries"),
		ToolsDir:        filepath.Join(runtimeRoot, "tools"),
		OwnerDir:        filepath.Join(runtimeRoot, "owner"),
		AgentsDir:       filepath.Join(runtimeRoot, "agents"),
		TeamsDir:        filepath.Join(runtimeRoot, "teams"),
		RootDir:         filepath.Join(runtimeRoot, "root"),
		AutomationsDir:  filepath.Join(runtimeRoot, "automations"),
		ChatsDir:        filepath.Join(runtimeRoot, "chats"),
		MemoryDir:       filepath.Join(runtimeRoot, "memory"),
		PanDir:          filepath.Join(runtimeRoot, "pan"),
		SkillsMarketDir: filepath.Join(runtimeRoot, "skills-market"),
	}
	return Config{
		Server: ServerConfig{Port: "11949"},
		Paths:  paths,
		Agents: CatalogConfig{ExternalDir: paths.AgentsDir},
		Teams:  CatalogConfig{ExternalDir: paths.TeamsDir},
		Skills: SkillCatalogConfig{
			CatalogConfig:  CatalogConfig{ExternalDir: paths.SkillsMarketDir},
			MaxPromptChars: 8000,
		},
		VisionRecognize: VisionRecognizeConfig{
			Enabled:        false,
			DefaultProfile: "general",
		},
		CoderSettings: CoderSettingsConfig{
			ACPProxies: map[string]CoderACPProxyConfig{},
		},
		Providers: CatalogConfig{ExternalDir: filepath.Join(paths.RegistriesDir, "providers")},
		Models:    CatalogConfig{ExternalDir: filepath.Join(paths.RegistriesDir, "models")},
		Automation: AutomationConfig{
			ExternalDir: paths.AutomationsDir,
			Enabled:     true,
			PoolSize:    4,
		},
		Memory: MemoryConfig{
			Enabled:            true,
			DBFileName:         "memory.db",
			ContextTopN:        5,
			ContextMaxChars:    4000,
			SearchDefaultLimit: 10,
			HybridVectorWeight: 0.7,
			HybridFTSWeight:    0.3,
			DualWriteMarkdown:  true,
			StorageDir:         paths.MemoryDir,
		},
		Defaults: DefaultsConfig{
			MaxTokens: 4096,
			Budget: BudgetDefaultsConfig{
				RunTimeoutMs: 300000,
				Model: RetryBudgetConfig{
					MaxCalls:   30,
					TimeoutMs:  120000,
					RetryCount: 0,
				},
				Tool: RetryBudgetConfig{
					MaxCalls:   20,
					TimeoutMs:  120000,
					RetryCount: 0,
				},
				Hitl: HitlBudgetConfig{
					TimeoutMs: 0,
				},
			},
			React: ReactDefaultsConfig{MaxSteps: 60},
			Plan: PlanExecuteDefaultsConfig{
				MaxSteps:             60,
				MaxWorkRoundsPerTask: 6,
			},
		},
		Stream: StreamConfig{
			IncludeToolPayloadEvents: true,
			DebugEventsEnabled:       false,
		},
		SSE: SSEConfig{
			HeartbeatIntervalMs: 15000,
		},
		H2A: H2AConfig{
			Render: H2ARenderConfig{
				FlushIntervalMs:      0,
				MaxBufferedChars:     0,
				MaxBufferedEvents:    0,
				HeartbeatPassThrough: true,
			},
		},
		Auth: AuthConfig{
			Enabled:            true,
			LocalPublicKeyFile: filepath.Join("configs", "local-public-key.pem"),
		},
		ResourceTicket: ResourceTicketConfig{
			Secret:     "",
			TTLSeconds: 86400,
		},
		ChatStorage: ChatStorageConfig{
			Dir:                                  paths.ChatsDir,
			K:                                    20,
			Charset:                              "UTF-8",
			ActionTools:                          nil,
			IndexSQLiteFile:                      "chats.db",
			IndexAutoRebuildOnIncompatibleSchema: true,
		},
		Logging: LoggingConfig{
			Request:   ToggleConfig{Enabled: true},
			Auth:      ToggleConfig{Enabled: true},
			Exception: ToggleConfig{Enabled: true},
			Tool:      ToggleConfig{Enabled: true},
			Action:    ToggleConfig{Enabled: true},
			Viewport:  ToggleConfig{Enabled: true},
			SSE:       ToggleConfig{Enabled: false},
			Memory: MemoryLoggingConfig{
				Enabled: true,
			},
			LLMInteraction: LLMInteractionLoggingConfig{
				Enabled:       true,
				MaskSensitive: false,
				RecordEnabled: false,
				RecordDir:     filepath.Join(paths.ChatsDir, "llm"),
			},
		},
		CORS: CORSConfig{
			Enabled:               false,
			PathPattern:           "/api/**",
			AllowedOriginPatterns: []string{"http://localhost:8081"},
			AllowedMethods:        []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:        []string{"*"},
			ExposedHeaders:        []string{"Content-Type"},
			AllowCredentials:      false,
			MaxAgeSeconds:         3600,
		},
		ContainerHub: ContainerHubConfig{
			Enabled:             false,
			RequestTimeoutMs:    300000,
			DefaultSandboxLevel: "run",
			AgentIdleTimeoutMs:  600000,
			DestroyQueueDelayMs: 5000,
		},
		Desktop: DesktopConfig{
			Action: DesktopBridgeConfig{
				RequestTimeoutMs: 20000,
			},
			CDP: DesktopBridgeConfig{
				RequestTimeoutMs: 20000,
			},
		},
		AccessPolicy: defaultAccessPolicyConfig(),
		Bash: BashConfig{
			WorkingDirectory: "",
			AllowedPaths:     []string{".", "/tmp"},
			AllowedCommands: []string{
				"ls", "pwd", "cat", "head", "tail", "top", "free", "df", "git", "rg", "find",
				"echo", "printf", "sed", "awk", "grep", "wc", "sort", "uniq", "tr", "cut", "xargs",
				"cd", "stat", "file", "du", "test", "which", "mkdir", "touch", "cp", "mv", "rm", "ln", "chmod",
				"env", "date", "bash", "sh",
				"make", "go", "npm", "yarn", "pnpm", "node", "python", "python3", "pip",
				"curl", "wget",
			},
			PathCheckedCommands:     []string{"ls", "cat", "head", "tail", "git", "rg", "find"},
			PathCheckBypassCommands: nil,
			ShellFeaturesEnabled:    true,
			ShellExecutable:         "",
			ShellArgs:               nil,
			ShellTimeoutMs:          10000,
			MaxCommandChars:         16000,
		},
		FileTools: FileToolsConfig{
			WorkingDirectory:       "",
			AllowedReadPaths:       nil,
			AllowedWritePaths:      nil,
			MaxReadBytes:           1 << 20,
			MaxWriteBytes:          1 << 20,
			MaxBatchOps:            20,
			RequireWriteApproval:   true,
			RequireReadBeforeWrite: true,
			Hooks: FileToolsHooksConfig{
				AfterFileChange: FileAfterChangeHooksConfig{
					LSPDiagnostics: defaultLSPDiagnosticsHookConfig(),
				},
			},
		},
		BashHITL: BashHITLConfig{
			DefaultTimeoutMs: 120000,
		},
		Run: RunConfig{
			ReaperIntervalMs:        30000,
			MaxBackgroundDurationMs: 600000,
			CompletedRetentionMs:    600000,
			EventBusMaxEvents:       10000,
			MaxDisconnectedWaitMs:   600000,
		},
		WebSocket: WebSocketConfig{
			MaxMessageSizeBytes: 1 << 20,
			PingIntervalMs:      30000,
			WriteTimeoutMs:      15000,
			WriteQueueSize:      256,
			MaxObservesPerConn:  8,
		},
	}
}

func memoryLogFileDefault(memoryDir string) string {
	if strings.TrimSpace(memoryDir) == "" {
		return ""
	}
	return filepath.Join(memoryDir, "memory.log")
}

func defaultLSPDiagnosticsHookConfig() LSPDiagnosticsHookConfig {
	return LSPDiagnosticsHookConfig{
		Enabled:   true,
		TimeoutMs: 3000,
		Languages: []string{"go", "typescript", "javascript", "python", "rust"},
		Servers: map[string]LSPServerConfig{
			"go":         {Command: "gopls"},
			"typescript": {Command: "typescript-language-server", Args: []string{"--stdio"}},
			"javascript": {Command: "typescript-language-server", Args: []string{"--stdio"}},
			"python":     {Command: "pyright-langserver", Args: []string{"--stdio"}},
			"rust":       {Command: "rust-analyzer"},
		},
	}
}

func defaultAccessPolicyConfig() AccessPolicyConfig {
	return AccessPolicyConfig{
		Version:          1,
		WorkingDirectory: "@workspace",
		Levels: map[string]AccessPolicyLevelConfig{
			"default": {
				ReadRoots:     []string{"@workspace", "@chat", "@agent", "@skills"},
				WriteRoots:    []string{"@workspace", "@chat"},
				ReadonlyRoots: []string{"@agent", "@skills", "@skills-market"},
				Approvals: AccessPolicyApprovalConfig{
					ReadOutsideRoots:      "hitl",
					WriteOutsideRoots:     "hitl",
					BashComplexFilesystem: "hitl",
					BashOpaqueCommand:     "hitl",
					BashWriteInWriteRoots: "allow",
				},
			},
			"auto_approve": {
				Inherit: "default",
				Approvals: AccessPolicyApprovalConfig{
					ReadOutsideRoots:      "auto",
					WriteOutsideRoots:     "hitl",
					BashComplexFilesystem: "auto",
					BashOpaqueCommand:     "auto",
				},
			},
			"full_access": {
				ReadRoots:     []string{"/"},
				WriteRoots:    []string{"/"},
				ReadonlyRoots: nil,
				Approvals: AccessPolicyApprovalConfig{
					ReadOutsideRoots:      "allow",
					WriteOutsideRoots:     "allow",
					BashComplexFilesystem: "allow",
					BashOpaqueCommand:     "allow",
					BashWriteInWriteRoots: "allow",
				},
			},
		},
	}
}

func (c *Config) normalize() error {
	c.Paths.RegistriesDir = filepath.Clean(c.Paths.RegistriesDir)
	c.Paths.ToolsDir = filepath.Clean(c.Paths.ToolsDir)
	c.Paths.OwnerDir = filepath.Clean(c.Paths.OwnerDir)
	c.Paths.AgentsDir = filepath.Clean(c.Paths.AgentsDir)
	c.Paths.TeamsDir = filepath.Clean(c.Paths.TeamsDir)
	c.Paths.RootDir = filepath.Clean(c.Paths.RootDir)
	c.Paths.AutomationsDir = filepath.Clean(c.Paths.AutomationsDir)
	c.Paths.ChatsDir = filepath.Clean(c.Paths.ChatsDir)
	c.Paths.MemoryDir = filepath.Clean(c.Paths.MemoryDir)
	c.Paths.PanDir = filepath.Clean(c.Paths.PanDir)
	c.Paths.SkillsMarketDir = filepath.Clean(c.Paths.SkillsMarketDir)

	c.Agents.ExternalDir = filepath.Clean(c.Paths.AgentsDir)
	c.Teams.ExternalDir = filepath.Clean(c.Paths.TeamsDir)
	c.Skills.ExternalDir = filepath.Clean(c.Paths.SkillsMarketDir)
	c.Automation.ExternalDir = filepath.Clean(c.Paths.AutomationsDir)
	c.Memory.StorageDir = filepath.Clean(c.Paths.MemoryDir)
	c.ChatStorage.Dir = filepath.Clean(c.Paths.ChatsDir)
	c.Providers.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "providers"))
	c.Models.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "models"))
	if strings.TrimSpace(c.Logging.Memory.File) != "" {
		c.Logging.Memory.File = filepath.Clean(c.Logging.Memory.File)
	}

	c.Auth.LocalPublicKeyFile = resolveAuthLocalPublicKeyFile(c.Auth.LocalPublicKeyFile)
	if c.ContainerHub.DefaultSandboxLevel == "" {
		c.ContainerHub.DefaultSandboxLevel = "run"
	}
	c.Desktop.Action = normalizeDesktopBridgeConfig(c.Desktop.Action)
	c.Desktop.CDP = normalizeDesktopBridgeConfig(c.Desktop.CDP)
	c.VisionRecognize = normalizeVisionRecognizeConfig(c.VisionRecognize)
	c.ContainerHub.Enabled = strings.TrimSpace(c.ContainerHub.BaseURL) != ""
	if c.Bash.WorkingDirectory == "" {
		c.Bash.WorkingDirectory = "."
	}
	c.AccessPolicy = normalizeAccessPolicyConfig(c.AccessPolicy)
	if c.FileTools.WorkingDirectory == "" {
		c.FileTools.WorkingDirectory = c.Bash.WorkingDirectory
	}
	if len(c.FileTools.AllowedReadPaths) == 0 {
		c.FileTools.AllowedReadPaths = []string{".", "/tmp"}
	}
	if len(c.FileTools.AllowedWritePaths) == 0 {
		c.FileTools.AllowedWritePaths = []string{".", "/tmp"}
	}
	if c.FileTools.MaxReadBytes <= 0 {
		c.FileTools.MaxReadBytes = 1 << 20
	}
	if c.FileTools.MaxWriteBytes <= 0 {
		c.FileTools.MaxWriteBytes = 1 << 20
	}
	if c.FileTools.MaxBatchOps <= 0 {
		c.FileTools.MaxBatchOps = 20
	}
	c.FileTools.Hooks.AfterFileChange.LSPDiagnostics = normalizeLSPDiagnosticsHookConfig(c.FileTools.Hooks.AfterFileChange.LSPDiagnostics)

	if err := c.normalizeChannels(); err != nil {
		return err
	}
	if err := c.normalizeGateways(); err != nil {
		return err
	}
	return nil
}

func normalizeDesktopBridgeConfig(cfg DesktopBridgeConfig) DesktopBridgeConfig {
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Path = strings.TrimSpace(cfg.Path)
	if cfg.RequestTimeoutMs <= 0 {
		cfg.RequestTimeoutMs = 20000
	}
	if cfg.Host == "" || cfg.Port <= 0 || cfg.Path == "" {
		cfg.BridgeURL = ""
		return cfg
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		cfg.Path = "/" + cfg.Path
	}
	cfg.BridgeURL = fmt.Sprintf("http://%s:%d%s", cfg.Host, cfg.Port, cfg.Path)
	return cfg
}

func normalizeVisionRecognizeConfig(cfg VisionRecognizeConfig) VisionRecognizeConfig {
	cfg.DefaultProfile = strings.TrimSpace(cfg.DefaultProfile)
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "general"
	}
	if len(cfg.Profiles) == 0 {
		return cfg
	}
	profiles := make(map[string]VisionRecognizeProfileConfig, len(cfg.Profiles))
	for key, profile := range cfg.Profiles {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		profile.ModelKey = strings.TrimSpace(profile.ModelKey)
		profile.OutputFormat = normalizeVisionOutputFormat(profile.OutputFormat)
		profile.SystemPrompt = strings.TrimSpace(profile.SystemPrompt)
		profiles[normalizedKey] = profile
	}
	cfg.Profiles = profiles
	return cfg
}

func normalizeVisionOutputFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return "json"
	default:
		return "text"
	}
}

func normalizeAccessPolicyConfig(cfg AccessPolicyConfig) AccessPolicyConfig {
	defaults := defaultAccessPolicyConfig()
	if cfg.Version <= 0 {
		cfg.Version = defaults.Version
	}
	if strings.TrimSpace(cfg.WorkingDirectory) == "" {
		cfg.WorkingDirectory = defaults.WorkingDirectory
	}
	if len(cfg.Levels) == 0 {
		cfg.Levels = defaults.Levels
	}
	for name, level := range defaults.Levels {
		if _, ok := cfg.Levels[name]; !ok {
			cfg.Levels[name] = level
		}
	}
	normalizedLevels := make(map[string]AccessPolicyLevelConfig, len(cfg.Levels))
	for name, level := range cfg.Levels {
		normalizedName := strings.ToLower(strings.TrimSpace(name))
		if normalizedName == "" {
			continue
		}
		level.Inherit = strings.ToLower(strings.TrimSpace(level.Inherit))
		level.ReadRoots = normalizeAccessPolicyRoots(level.ReadRoots)
		level.WriteRoots = normalizeAccessPolicyRoots(level.WriteRoots)
		level.ReadonlyRoots = normalizeAccessPolicyRoots(level.ReadonlyRoots)
		level.Approvals = normalizeAccessPolicyApprovals(level.Approvals)
		normalizedLevels[normalizedName] = level
	}
	cfg.Levels = normalizedLevels
	return cfg
}

func normalizeAccessPolicyRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !strings.HasPrefix(root, "@") {
			root = filepath.Clean(root)
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

func normalizeAccessPolicyApprovals(approvals AccessPolicyApprovalConfig) AccessPolicyApprovalConfig {
	approvals.ReadOutsideRoots = normalizeAccessPolicyApprovalAction(approvals.ReadOutsideRoots, "hitl")
	approvals.WriteOutsideRoots = normalizeAccessPolicyApprovalAction(approvals.WriteOutsideRoots, "hitl")
	approvals.BashComplexFilesystem = normalizeAccessPolicyApprovalAction(approvals.BashComplexFilesystem, "hitl")
	approvals.BashOpaqueCommand = normalizeAccessPolicyApprovalAction(approvals.BashOpaqueCommand, "hitl")
	approvals.BashWriteInWriteRoots = normalizeAccessPolicyApprovalAction(approvals.BashWriteInWriteRoots, "allow")
	return approvals
}

func normalizeAccessPolicyApprovalAction(value string, fallback string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "allow", "hitl", "auto", "block":
		return normalized
	default:
		return fallback
	}
}

func (c *Config) normalizeChannels() error {
	if len(c.Channels) == 0 {
		return nil
	}
	seenChannelIDs := map[string]struct{}{}
	existingGatewayIDs := map[string]struct{}{}
	existingGatewayChannels := map[string]struct{}{}
	for _, gateway := range c.Gateways {
		id := strings.TrimSpace(gateway.ID)
		if id != "" {
			existingGatewayIDs[id] = struct{}{}
		}
		channel := strings.TrimSpace(gateway.Channel)
		if channel != "" {
			existingGatewayChannels[channel] = struct{}{}
		}
	}

	for _, channelCfg := range c.Channels {
		channelID := strings.TrimSpace(channelCfg.ID)
		if channelID == "" {
			return fmt.Errorf("channels config: channel id must not be empty")
		}
		if _, exists := seenChannelIDs[channelID]; exists {
			return fmt.Errorf("channels config: duplicate channel id %q", channelID)
		}
		seenChannelIDs[channelID] = struct{}{}
		if _, exists := existingGatewayChannels[channelID]; exists {
			return fmt.Errorf("channels config: channel %q conflicts with an existing gateway channel", channelID)
		}
		if _, exists := existingGatewayIDs[channelID]; exists {
			return fmt.Errorf("channels config: channel %q conflicts with an existing gateway id", channelID)
		}
		if strings.TrimSpace(channelCfg.Gateway.URL) == "" {
			return fmt.Errorf("channels config: channel %q gateway.url is required", channelID)
		}
		c.Gateways = append(c.Gateways, GatewayEntry{
			ID:                 channelID,
			Channel:            channelID,
			SourceChannel:      deriveSourceChannelFromURL(channelCfg.Gateway.URL),
			SourcePrefix:       deriveChannelFromURL(channelCfg.Gateway.URL),
			URL:                strings.TrimSpace(channelCfg.Gateway.URL),
			JwtToken:           strings.TrimSpace(channelCfg.Gateway.JwtToken),
			BaseURL:            strings.TrimSpace(channelCfg.Gateway.BaseURL),
			HandshakeTimeoutMs: channelCfg.Gateway.HandshakeTimeoutMs,
			ReconnectMinMs:     channelCfg.Gateway.ReconnectMinMs,
			ReconnectMaxMs:     channelCfg.Gateway.ReconnectMaxMs,
		})
		existingGatewayIDs[channelID] = struct{}{}
		existingGatewayChannels[channelID] = struct{}{}
	}
	return nil
}

func (c *Config) normalizeGateways() error {
	for i := range c.Gateways {
		g := &c.Gateways[i]
		if strings.TrimSpace(g.ID) == "" {
			g.ID = fmt.Sprintf("gateway-%d", i)
		}
		if g.HandshakeTimeoutMs == 0 {
			g.HandshakeTimeoutMs = defaultGatewayHandshakeTimeoutMs
		}
		if g.ReconnectMinMs == 0 {
			g.ReconnectMinMs = defaultGatewayReconnectMinMs
		}
		if g.ReconnectMaxMs == 0 {
			g.ReconnectMaxMs = defaultGatewayReconnectMaxMs
		}
		if strings.TrimSpace(g.BaseURL) == "" && strings.TrimSpace(g.URL) != "" {
			if parsed, err := neturl.Parse(strings.TrimSpace(g.URL)); err == nil && parsed.Host != "" {
				scheme := "http"
				if parsed.Scheme == "wss" {
					scheme = "https"
				}
				g.BaseURL = scheme + "://" + parsed.Host
			}
		}
		if strings.TrimSpace(g.Channel) == "" {
			g.Channel = deriveChannelFromURL(g.URL)
		}
		if strings.TrimSpace(g.SourceChannel) == "" {
			g.SourceChannel = deriveSourceChannelFromURL(g.URL)
		}
		if strings.TrimSpace(g.SourcePrefix) == "" {
			g.SourcePrefix = sourcePrefix(g.SourceChannel)
		}
	}
	seenIDs := map[string]struct{}{}
	seenChannels := map[string]struct{}{}
	seenSourceChannels := map[string]struct{}{}
	for _, gateway := range c.Gateways {
		id := strings.TrimSpace(gateway.ID)
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("gateway config: duplicate id %q", id)
		}
		seenIDs[id] = struct{}{}
		channel := strings.TrimSpace(gateway.Channel)
		if channel != "" {
			if _, exists := seenChannels[channel]; exists {
				return fmt.Errorf("gateway config: duplicate channel %q", channel)
			}
			seenChannels[channel] = struct{}{}
		}
		sourceChannel := strings.TrimSpace(gateway.SourceChannel)
		if sourceChannel == "" {
			continue
		}
		if _, exists := seenSourceChannels[sourceChannel]; exists {
			return fmt.Errorf("gateway config: duplicate source channel %q", sourceChannel)
		}
		seenSourceChannels[sourceChannel] = struct{}{}
	}
	return nil
}

// deriveChannelFromURL 从 gateway URL 的 ?channel=xxx 参数提取 channel 名；
// channel 值形如 "wecom:xiaozhai" 时只取冒号前的 "wecom" 作为路由键。
// 解析失败或缺失时返回空串（= 默认条目，命中所有未匹配前缀的 chatId）。

func deriveChannelFromURL(raw string) string {
	return sourcePrefix(deriveSourceChannelFromURL(raw))
}

func deriveSourceChannelFromURL(raw string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("channel"))
}

func sourcePrefix(ch string) string {
	ch = strings.TrimSpace(ch)
	if ch == "" {
		return ""
	}
	if idx := strings.Index(ch, ":"); idx > 0 {
		return ch[:idx]
	}
	return ch
}
