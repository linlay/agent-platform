package config

import (
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func defaultConfig(options LoadOptions) Config {
	runtimeMode, _ := ParseRuntimeMode(options.RuntimeMode)
	runtimeRoot := defaultRuntimeRoot()
	paths := PathsConfig{
		RegistriesDir:   filepath.Join(runtimeRoot, "registries"),
		ToolsDir:        filepath.Join(runtimeRoot, "tools"),
		OwnerDir:        filepath.Join(runtimeRoot, "owner"),
		AgentsDir:       filepath.Join(runtimeRoot, "agents"),
		RUAgentsDir:     filepath.Join(runtimeRoot, "ru-agents"),
		TeamsDir:        filepath.Join(runtimeRoot, "teams"),
		RootDir:         filepath.Join(runtimeRoot, "root"),
		AutomationsDir:  filepath.Join(runtimeRoot, "automations"),
		ChatsDir:        filepath.Join(runtimeRoot, "chats"),
		MemoryDir:       filepath.Join(runtimeRoot, "memory"),
		KBaseDir:        filepath.Join(runtimeRoot, "kbase"),
		PanDir:          filepath.Join(runtimeRoot, "pan"),
		SkillsCenterDir: filepath.Join(runtimeRoot, "skills-center"),
		RunStateDir:     filepath.Join(runtimeRoot, "run-state"),
	}
	return Config{
		IdentityFile: options.IdentityFile,
		RuntimeMode:  runtimeMode,
		Server:       ServerConfig{Port: "8080"},
		Paths:        paths,
		Agents:       CatalogConfig{ExternalDir: paths.AgentsDir},
		Teams:        CatalogConfig{ExternalDir: paths.TeamsDir},
		Skills: SkillCatalogConfig{
			CatalogConfig:  CatalogConfig{ExternalDir: paths.SkillsCenterDir},
			MaxPromptChars: 8000,
		},
		VisionRecognize: VisionRecognizeConfig{
			Enabled:        false,
			DefaultProfile: "general",
		},
		WebFetch: WebFetchConfig{
			Enabled:        false,
			DefaultProfile: "general",
		},
		ImageGenerate: ImageGenerateConfig{
			Enabled:        false,
			DefaultProfile: "general",
		},
		CoderSettings: CoderSettingsConfig{
			ACPBridges: map[string]CoderACPBridgeConfig{},
		},
		KBase: KBaseConfig{
			Index: KBaseIndexConfig{
				FTS: KBaseFTSIndexConfig{
					BaseTokenizer: "icu",
				},
				Vector: KBaseVectorIndexConfig{
					ANNMinRows: 50000,
				},
			},
			Maintenance: KBaseMaintenanceConfig{
				OptimizeChangeThreshold: 1000,
				OptimizeInterval:        24 * time.Hour,
				VersionRetention:        7 * 24 * time.Hour,
			},
			Refresh: KBaseRefreshConfig{
				Debounce:          2 * time.Second,
				ReconcileInterval: 10 * time.Minute,
			},
			Extraction: KBaseExtractionConfig{
				Timeout:      60 * time.Second,
				MaxFileBytes: 50 * 1024 * 1024,
				PDF: KBasePDFExtractionConfig{
					Enabled: true,
					Backend: "poppler",
					Binary:  "pdftotext",
				},
				DOCX: KBaseDOCXExtractionConfig{
					Enabled: true,
					Backend: "native",
				},
				PPTX: KBasePPTXExtractionConfig{
					Enabled:      true,
					Backend:      "native",
					IncludeNotes: true,
				},
			},
		},
		Providers: CatalogConfig{ExternalDir: filepath.Join(paths.RegistriesDir, "providers")},
		Models:    CatalogConfig{ExternalDir: filepath.Join(paths.RegistriesDir, "models")},
		Automation: AutomationConfig{
			ExternalDir: paths.AutomationsDir,
			Enabled:     true,
			PoolSize:    4,
		},
		Billing: BillingConfig{
			Currency: "CNY",
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
			Budget: BudgetDefaultsConfig{
				Timeout:  3600,
				MaxSteps: 100,
				Model: RetryBudgetConfig{
					MaxCalls:   100,
					Timeout:    180,
					RetryCount: 5,
				},
				Tool: RetryBudgetConfig{
					MaxCalls:   100,
					Timeout:    600,
					RetryCount: 0,
				},
				Hitl: HitlBudgetConfig{
					Timeout: 0,
				},
			},
			React: ReactDefaultsConfig{MaxSteps: 60},
			Plan: PlanExecuteDefaultsConfig{
				MaxSteps:             60,
				MaxWorkRoundsPerTask: 6,
			},
			CoderPlanning: CoderPlanningDefaultsConfig{MaxSteps: 60},
		},
		SSE: SSEConfig{
			HeartbeatInterval: 30, // seconds
		},
		Auth: AuthConfig{
			Enabled:            true,
			LocalPublicKeyFile: filepath.Join("configs", "local-public-key.pem"),
		},
		ResourceTicket: ResourceTicketConfig{
			Secret:     "",
			TTLSeconds: 86400,
		},
		Logging: defaultLoggingConfig(paths.ChatsDir, paths.MemoryDir),
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
			RequestTimeout:      300,
			DefaultSandboxLevel: "run",
			AgentIdleTimeout:    600,
			DestroyQueueDelay:   5,
		},
		AccessPolicy: defaultAccessPolicyConfig(),
		Bash: BashConfig{
			AllowedCommands: []string{
				"ls", "pwd", "cat", "head", "tail", "top", "free", "df", "git", "rg", "find",
				"echo", "printf", "sed", "awk", "grep", "wc", "sort", "uniq", "tr", "cut", "xargs",
				"cd", "stat", "file", "du", "test", "which", "mkdir", "touch", "cp", "mv", "rm", "ln", "chmod",
				"env", "date", "bash", "sh",
				"make", "go", "npm", "yarn", "pnpm", "node", "python", "python3", "pip",
				"curl", "wget",
			},
			ShellFeaturesEnabled: true,
			ShellExecutable:      "",
			ShellArgs:            nil,
			MaxCommandChars:      16000,
		},
		FileTools: FileToolsConfig{
			MaxReadBytes:           1 << 20,
			MaxWriteBytes:          1 << 20,
			MaxBatchOps:            20,
			RequireWriteApproval:   true,
			RequireReadBeforeWrite: true,
			ReadBeforeWriteScope:   "run",
			Hooks: FileToolsHooksConfig{
				AfterFileChange: FileAfterChangeHooksConfig{
					LSPDiagnostics: defaultLSPDiagnosticsHookConfig(),
				},
			},
		},
		PlatformControl: PlatformControlConfig{
			Enabled:           true,
			MaxDynamicKeys:    32,
			MaxValueBytes:     4096,
			MaxTotalBytes:     32768,
			MaxBulkOperations: 16,
			CheckpointKeyFile: filepath.Join(runtimeRoot, "identity", "run-env.key"),
		},
	}
}

func defaultRuntimeRoot() string {
	runtimeRoot := strings.TrimSpace(os.Getenv("AP_RUNTIME_DIR"))
	if runtimeRoot == "" {
		return "runtime"
	}
	return runtimeRoot
}

func resolveIdentityFile(configRoot string, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("identity file must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}

	runtimeRoot, err := expandRuntimeRootHome(defaultRuntimeRoot())
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(runtimeRoot) {
		runtimeRoot = filepath.Join(resolveConfigRoot(configRoot), runtimeRoot)
	}
	identityFile, err := filepath.Abs(filepath.Join(runtimeRoot, "identity", "access-token"))
	if err != nil {
		return "", fmt.Errorf("resolve default identity file: %w", err)
	}
	return filepath.Clean(identityFile), nil
}

func expandRuntimeRootHome(runtimeRoot string) (string, error) {
	if runtimeRoot != "~" && !strings.HasPrefix(runtimeRoot, "~/") && !strings.HasPrefix(runtimeRoot, `~\`) {
		return runtimeRoot, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		if err == nil {
			err = fmt.Errorf("home directory is empty")
		}
		return "", fmt.Errorf("expand AP_RUNTIME_DIR home: %w", err)
	}
	if runtimeRoot == "~" {
		return home, nil
	}
	return filepath.Join(home, runtimeRoot[2:]), nil
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
		Timeout:   3,
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
		Levels: map[string]AccessPolicyLevelConfig{
			"default": {
				ReadRoots:     []string{"@workspace", "@chat", "@agent", "@skills", "@temp"},
				WriteRoots:    []string{"@workspace", "@chat", "@temp"},
				ReadonlyRoots: []string{"@agent", "@skills", "@skills-center"},
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
					BashWriteInWriteRoots: "allow",
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

func (c *Config) normalize(configRoot string) error {
	c.Paths.RegistriesDir = filepath.Clean(c.Paths.RegistriesDir)
	c.Paths.ToolsDir = filepath.Clean(c.Paths.ToolsDir)
	c.Paths.OwnerDir = filepath.Clean(c.Paths.OwnerDir)
	c.Paths.AgentsDir = filepath.Clean(c.Paths.AgentsDir)
	ruAgentsDir := filepath.Clean(c.Paths.RUAgentsDir)
	if !filepath.IsAbs(ruAgentsDir) {
		ruAgentsDir = filepath.Join(resolveConfigRoot(configRoot), ruAgentsDir)
	}
	ruAgentsDir, err := filepath.Abs(ruAgentsDir)
	if err != nil {
		return fmt.Errorf("resolve paths.ru-agents-dir: %w", err)
	}
	c.Paths.RUAgentsDir = ruAgentsDir
	c.Paths.TeamsDir = filepath.Clean(c.Paths.TeamsDir)
	c.Paths.RootDir = filepath.Clean(c.Paths.RootDir)
	c.Paths.AutomationsDir = filepath.Clean(c.Paths.AutomationsDir)
	c.Paths.ChatsDir = filepath.Clean(c.Paths.ChatsDir)
	c.Paths.MemoryDir = filepath.Clean(c.Paths.MemoryDir)
	c.Paths.KBaseDir = filepath.Clean(c.Paths.KBaseDir)
	c.Paths.PanDir = filepath.Clean(c.Paths.PanDir)
	c.Paths.SkillsCenterDir = filepath.Clean(c.Paths.SkillsCenterDir)
	c.Paths.RunStateDir = filepath.Clean(c.Paths.RunStateDir)
	c.PlatformControl.CheckpointKeyFile = filepath.Clean(c.PlatformControl.CheckpointKeyFile)

	c.Agents.ExternalDir = filepath.Clean(c.Paths.AgentsDir)
	c.Teams.ExternalDir = filepath.Clean(c.Paths.TeamsDir)
	c.Skills.ExternalDir = filepath.Clean(c.Paths.SkillsCenterDir)
	c.Automation.ExternalDir = filepath.Clean(c.Paths.AutomationsDir)
	c.Memory.StorageDir = filepath.Clean(c.Paths.MemoryDir)
	c.Logging.LLMInteraction.RecordDir = filepath.Clean(c.Paths.ChatsDir)
	c.Providers.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "providers"))
	c.Models.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "models"))
	c.Logging.Memory.File = memoryLogFileDefault(c.Paths.MemoryDir)
	if strings.TrimSpace(c.Logging.Memory.File) != "" {
		c.Logging.Memory.File = filepath.Clean(c.Logging.Memory.File)
	}

	c.Auth.LocalPublicKeyFile = fixedAuthLocalPublicKeyFile(configRoot)
	if strings.TrimSpace(c.Auth.JWKSURI) != "" {
		c.Auth.LocalPublicKeyFile = ""
	}
	if c.ContainerHub.DefaultSandboxLevel == "" {
		c.ContainerHub.DefaultSandboxLevel = "run"
	}
	c.VisionRecognize = normalizeVisionRecognizeConfig(c.VisionRecognize)
	c.WebFetch = normalizeWebFetchConfig(c.WebFetch)
	c.ImageGenerate = normalizeImageGenerateConfig(c.ImageGenerate)
	if err := normalizeKBaseConfig(&c.KBase); err != nil {
		return err
	}
	c.ContainerHub.Enabled = strings.TrimSpace(c.ContainerHub.BaseURL) != ""
	c.AccessPolicy = normalizeAccessPolicyConfig(c.AccessPolicy)
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

func normalizeKBaseExtractionConfig(cfg KBaseExtractionConfig) KBaseExtractionConfig {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 50 * 1024 * 1024
	}
	cfg.PDF.Backend = strings.ToLower(strings.TrimSpace(cfg.PDF.Backend))
	if cfg.PDF.Backend == "" {
		cfg.PDF.Backend = "poppler"
	}
	cfg.PDF.Binary = strings.TrimSpace(cfg.PDF.Binary)
	if cfg.PDF.Binary == "" {
		cfg.PDF.Binary = "pdftotext"
	}
	cfg.DOCX.Backend = strings.ToLower(strings.TrimSpace(cfg.DOCX.Backend))
	if cfg.DOCX.Backend == "" {
		cfg.DOCX.Backend = "native"
	}
	cfg.PPTX.Backend = strings.ToLower(strings.TrimSpace(cfg.PPTX.Backend))
	if cfg.PPTX.Backend == "" {
		cfg.PPTX.Backend = "native"
	}
	return cfg
}

func normalizeKBaseConfig(cfg *KBaseConfig) error {
	if cfg == nil {
		return nil
	}
	cfg.Index.FTS.BaseTokenizer = strings.ToLower(strings.TrimSpace(cfg.Index.FTS.BaseTokenizer))
	if cfg.Index.FTS.BaseTokenizer == "" {
		cfg.Index.FTS.BaseTokenizer = "icu"
	}
	if cfg.Index.Vector.ANNMinRows < 1000 {
		return fmt.Errorf("kbase index.vector.ann-min-rows must be at least 1000")
	}
	if cfg.Maintenance.OptimizeChangeThreshold < 1 {
		return fmt.Errorf("kbase maintenance.optimize-change-threshold must be at least 1")
	}
	if cfg.Maintenance.OptimizeInterval <= 0 {
		return fmt.Errorf("kbase maintenance.optimize-interval must be positive")
	}
	if cfg.Maintenance.VersionRetention <= 0 {
		return fmt.Errorf("kbase maintenance.version-retention must be positive")
	}
	cfg.Extraction = normalizeKBaseExtractionConfig(cfg.Extraction)
	return nil
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

func normalizeWebFetchConfig(cfg WebFetchConfig) WebFetchConfig {
	cfg.DefaultProfile = strings.TrimSpace(cfg.DefaultProfile)
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "general"
	}
	cfg.PreapprovedHosts = normalizeWebFetchHosts(cfg.PreapprovedHosts)
	if len(cfg.Profiles) == 0 {
		return cfg
	}
	profiles := make(map[string]WebFetchProfileConfig, len(cfg.Profiles))
	for key, profile := range cfg.Profiles {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		profile.ModelKey = strings.TrimSpace(profile.ModelKey)
		if profile.Timeout <= 0 {
			profile.Timeout = 60
		}
		if profile.FetchTimeout <= 0 {
			profile.FetchTimeout = 60
		}
		if profile.MaxURLLength <= 0 {
			profile.MaxURLLength = 2000
		}
		if profile.MaxResponseBytes <= 0 {
			profile.MaxResponseBytes = 10 * 1024 * 1024
		}
		if profile.MaxMarkdownChars <= 0 {
			profile.MaxMarkdownChars = 100000
		}
		if profile.MaxOutputTokens <= 0 {
			profile.MaxOutputTokens = 1200
		}
		profile.SystemPrompt = strings.TrimSpace(profile.SystemPrompt)
		profiles[normalizedKey] = profile
	}
	cfg.Profiles = profiles
	return cfg
}

func normalizeWebFetchHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	seen := map[string]struct{}{}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		host = strings.TrimSuffix(host, ".")
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	return out
}

func normalizeImageGenerateConfig(cfg ImageGenerateConfig) ImageGenerateConfig {
	cfg.DefaultProfile = strings.TrimSpace(cfg.DefaultProfile)
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "general"
	}
	if len(cfg.Profiles) == 0 {
		return cfg
	}
	profiles := make(map[string]ImageGenerateProfileConfig, len(cfg.Profiles))
	for key, profile := range cfg.Profiles {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		profile = normalizeImageGenerateProfileConfig(profile)
		profiles[normalizedKey] = profile
	}
	cfg.Profiles = profiles
	return cfg
}

func normalizeImageGenerateProfileConfig(profile ImageGenerateProfileConfig) ImageGenerateProfileConfig {
	profile.ModelKey = strings.TrimSpace(profile.ModelKey)
	profile.Size = strings.TrimSpace(profile.Size)
	if profile.Size == "" {
		profile.Size = "1024x1024"
	}
	profile.ResponseFormat = normalizeImageGenerateResponseFormat(profile.ResponseFormat)
	profile.OutputMimeType = strings.ToLower(strings.TrimSpace(profile.OutputMimeType))
	if profile.OutputMimeType == "" {
		profile.OutputMimeType = "image/png"
	}
	if profile.MaxPromptChars <= 0 {
		profile.MaxPromptChars = 4000
	}
	if profile.MaxImages <= 0 {
		profile.MaxImages = 4
	}
	if profile.MaxImageBytes <= 0 {
		profile.MaxImageBytes = 20 << 20
	}
	return profile
}

func normalizeImageGenerateResponseFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "url":
		return "url"
	default:
		return "b64_json"
	}
}

func normalizeAccessPolicyConfig(cfg AccessPolicyConfig) AccessPolicyConfig {
	defaults := defaultAccessPolicyConfig()
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
	if level, ok := normalizedLevels["default"]; ok {
		level.ReadRoots = appendAccessPolicyRoot(level.ReadRoots, "@temp")
		level.WriteRoots = appendAccessPolicyRoot(level.WriteRoots, "@temp")
		normalizedLevels["default"] = level
	}
	cfg.Levels = normalizedLevels
	return cfg
}

func appendAccessPolicyRoot(roots []string, required string) []string {
	for _, root := range roots {
		if strings.EqualFold(strings.TrimSpace(root), required) {
			return roots
		}
	}
	return append(roots, required)
}

func normalizeAccessPolicyRoots(roots []string) []string {
	if roots == nil {
		return nil
	}
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
		channelCfg = normalizeChannelConfigDefaults(channelCfg)
		c.Channels = replaceChannelConfig(c.Channels, channelID, channelCfg)
		if err := validateNormalizedChannelConfig(channelCfg); err != nil {
			return err
		}
		clientURL := strings.TrimSpace(channelClientURL(channelCfg))
		if clientURL == "" {
			continue
		}
		if _, exists := existingGatewayChannels[channelID]; exists {
			return fmt.Errorf("channels config: channel %q conflicts with an existing gateway channel", channelID)
		}
		if _, exists := existingGatewayIDs[channelID]; exists {
			return fmt.Errorf("channels config: channel %q conflicts with an existing gateway id", channelID)
		}
		c.Gateways = append(c.Gateways, GatewayEntry{
			ID:               channelID,
			Channel:          channelID,
			SourceChannel:    deriveSourceChannelFromURL(clientURL),
			SourcePrefix:     deriveChannelFromURL(clientURL),
			URL:              clientURL,
			JwtToken:         channelClientToken(channelCfg),
			HandshakeTimeout: channelCfg.Reconnect.HandshakeTimeout,
			ReconnectMin:     channelCfg.Reconnect.Min,
			ReconnectMax:     channelCfg.Reconnect.Max,
		})
		existingGatewayIDs[channelID] = struct{}{}
		existingGatewayChannels[channelID] = struct{}{}
	}
	return nil
}

func normalizeChannelConfigDefaults(channelCfg ChannelConfig) ChannelConfig {
	if strings.TrimSpace(channelCfg.Transport) == "" {
		channelCfg.Transport = ChannelTransportWebSocket
	}
	channelCfg.Transport = strings.ToLower(strings.TrimSpace(channelCfg.Transport))
	if strings.TrimSpace(channelCfg.Protocol) == "" {
		channelCfg.Protocol = ChannelProtocolPlatformWS
	}
	channelCfg.Protocol = strings.ToLower(strings.TrimSpace(channelCfg.Protocol))
	if strings.TrimSpace(string(channelCfg.Mode)) == "" {
		switch {
		case strings.TrimSpace(channelCfg.Endpoint.URL) != "":
			channelCfg.Mode = ChannelModeClient
		case strings.TrimSpace(channelCfg.Endpoint.Path) != "":
			channelCfg.Mode = ChannelModeServer
		default:
			channelCfg.Mode = ChannelModeClient
		}
	}
	channelCfg.Mode = ChannelMode(strings.ToLower(strings.TrimSpace(string(channelCfg.Mode))))
	return channelCfg
}

func replaceChannelConfig(channels []ChannelConfig, channelID string, normalized ChannelConfig) []ChannelConfig {
	for i := range channels {
		if strings.TrimSpace(channels[i].ID) == channelID {
			channels[i] = normalized
			return channels
		}
	}
	return channels
}

func validateNormalizedChannelConfig(channelCfg ChannelConfig) error {
	channelID := strings.TrimSpace(channelCfg.ID)
	switch channelCfg.Mode {
	case ChannelModeClient:
		if strings.TrimSpace(channelClientURL(channelCfg)) == "" {
			return fmt.Errorf("channels config: channel %q endpoint.url is required for client mode", channelID)
		}
	case ChannelModeServer:
		if strings.TrimSpace(channelCfg.Endpoint.Path) == "" {
			return fmt.Errorf("channels config: channel %q endpoint.path is required for server mode", channelID)
		}
	default:
		return fmt.Errorf("channels config: channel %q has invalid mode %q", channelID, channelCfg.Mode)
	}
	if channelCfg.Transport != ChannelTransportWebSocket {
		return fmt.Errorf("channels config: channel %q has unsupported transport %q", channelID, channelCfg.Transport)
	}
	if channelCfg.Protocol != ChannelProtocolPlatformWS {
		return fmt.Errorf("channels config: channel %q has unsupported protocol %q", channelID, channelCfg.Protocol)
	}
	return nil
}

func channelClientURL(channelCfg ChannelConfig) string {
	return strings.TrimSpace(channelCfg.Endpoint.URL)
}

func channelClientToken(channelCfg ChannelConfig) string {
	return strings.TrimSpace(channelCfg.Endpoint.Token)
}

func (c *Config) normalizeGateways() error {
	for i := range c.Gateways {
		g := &c.Gateways[i]
		if strings.TrimSpace(g.ID) == "" {
			g.ID = fmt.Sprintf("gateway-%d", i)
		}
		if g.HandshakeTimeout == 0 {
			g.HandshakeTimeout = defaultGatewayHandshakeTimeout
		}
		if g.ReconnectMin == 0 {
			g.ReconnectMin = defaultGatewayReconnectMin
		}
		if g.ReconnectMax == 0 {
			g.ReconnectMax = defaultGatewayReconnectMax
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
