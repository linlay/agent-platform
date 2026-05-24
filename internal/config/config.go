package config

import (
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Server         ServerConfig
	Paths          PathsConfig
	Agents         CatalogConfig
	Teams          CatalogConfig
	Skills         SkillCatalogConfig
	Prompts        PromptsConfig
	CoderPrompts   CoderPromptsConfig
	MemoryPrompts  MemoryPromptsConfig
	CoderSettings  CoderSettingsConfig
	Providers      CatalogConfig
	Models         CatalogConfig
	Automation     AutomationConfig
	Memory         MemoryConfig
	Defaults       DefaultsConfig
	Stream         StreamConfig
	SSE            SSEConfig
	H2A            H2AConfig
	Auth           AuthConfig
	ResourceTicket ResourceTicketConfig
	ChatStorage    ChatStorageConfig
	Logging        LoggingConfig
	CORS           CORSConfig
	ContainerHub   ContainerHubConfig
	Desktop        DesktopConfig
	AccessPolicy   AccessPolicyConfig
	Bash           BashConfig
	FileTools      FileToolsConfig
	BashHITL       BashHITLConfig
	Run            RunConfig
	WebSocket      WebSocketConfig
	GatewayWS      GatewayWSConfig
	// Gateways 是多 gateway 反向连接列表（wecom / feishu / ding / ...）。
	// legacy 单 gateway 配置 (GatewayWS) 在 normalize() 阶段合成为 Gateways[0]。
	Gateways []GatewayEntry
	// Channels 是 channel 元数据与 agent 准入配置；每条可合成一条 gateway entry。
	Channels []ChannelConfig
}

type ServerConfig struct {
	Port string
}

type PathsConfig struct {
	RegistriesDir   string
	ToolsDir        string
	OwnerDir        string
	AgentsDir       string
	TeamsDir        string
	RootDir         string
	AutomationsDir  string
	ChatsDir        string
	MemoryDir       string
	PanDir          string
	SkillsMarketDir string
}

type CatalogConfig struct {
	ExternalDir string
}

type SkillCatalogConfig struct {
	CatalogConfig
	MaxPromptChars int
}

type PromptsConfig struct {
	Skill        PromptSkillConfig
	ToolAppendix ToolAppendixPromptsConfig
	PlanExecute  PlanExecutePromptsConfig
}

type PromptSkillConfig struct {
	InstructionsPrompt string
	CatalogHeader      string
	DisclosureHeader   string
	InstructionsLabel  string
}

type ToolAppendixPromptsConfig struct {
	ToolDescriptionTitle string
	AfterCallHintTitle   string
}

type PlanExecutePromptsConfig struct {
	TaskExecutionPromptTemplate string
	PlanUserPromptTemplate      string
	SummarySystemPrompt         string
	SummaryUserPromptTemplate   string
}

type CoderPromptsConfig struct {
	PlanningPrompt            string
	SummarySystemPrompt       string
	SummaryUserPromptTemplate string
}

type MemoryPromptsConfig struct {
	SystemPromptTemplate string
	UserPromptTemplate   string
}

type CoderSettingsConfig struct {
	WorkspaceAgents CoderWorkspaceAgentsConfig
}

type CoderWorkspaceAgentsConfig struct {
	Enabled bool
	File    string
}

type AutomationConfig struct {
	ExternalDir   string
	Enabled       bool
	DefaultZoneID string
	PoolSize      int
}

type MemoryConfig struct {
	Enabled            bool
	DBFileName         string
	ContextTopN        int
	ContextMaxChars    int
	SearchDefaultLimit int
	HybridVectorWeight float64
	HybridFTSWeight    float64
	DualWriteMarkdown  bool
	StorageDir         string
}

type DefaultsConfig struct {
	MaxTokens int
	Budget    BudgetDefaultsConfig
	React     ReactDefaultsConfig
	Plan      PlanExecuteDefaultsConfig
}

type BudgetDefaultsConfig struct {
	RunTimeoutMs int
	Model        RetryBudgetConfig
	Tool         RetryBudgetConfig
	Hitl         HitlBudgetConfig
}

type RetryBudgetConfig struct {
	MaxCalls   int
	TimeoutMs  int
	RetryCount int
}

type HitlBudgetConfig struct {
	TimeoutMs int
}

type ReactDefaultsConfig struct {
	MaxSteps int
}

type PlanExecuteDefaultsConfig struct {
	MaxSteps             int
	MaxWorkRoundsPerTask int
}

type StreamConfig struct {
	IncludeToolPayloadEvents bool
	DebugEventsEnabled       bool
}

type SSEConfig struct {
	HeartbeatIntervalMs int64
}

type H2AConfig struct {
	Render H2ARenderConfig
}

type H2ARenderConfig struct {
	FlushIntervalMs      int64
	MaxBufferedChars     int
	MaxBufferedEvents    int
	HeartbeatPassThrough bool
}

type AuthConfig struct {
	Enabled            bool
	JWKSURI            string
	Issuer             string
	JWKSCacheSeconds   int
	LocalPublicKeyFile string
}

type ResourceTicketConfig struct {
	Secret     string
	TTLSeconds int64
}

func (c ResourceTicketConfig) Enabled() bool {
	return strings.TrimSpace(c.Secret) != ""
}

type ChatStorageConfig struct {
	Dir                                  string
	K                                    int
	Charset                              string
	ActionTools                          []string
	IndexSQLiteFile                      string
	IndexAutoRebuildOnIncompatibleSchema bool
}

type LoggingConfig struct {
	Request        ToggleConfig
	Auth           ToggleConfig
	Exception      ToggleConfig
	Tool           ToggleConfig
	Action         ToggleConfig
	Viewport       ToggleConfig
	SSE            ToggleConfig
	Memory         MemoryLoggingConfig
	LLMInteraction LLMInteractionLoggingConfig
}

type ToggleConfig struct {
	Enabled bool
}

type MemoryLoggingConfig struct {
	Enabled bool
	File    string
}

type LLMInteractionLoggingConfig struct {
	Enabled       bool
	MaskSensitive bool
}

type CORSConfig struct {
	Enabled               bool
	PathPattern           string
	AllowedOriginPatterns []string
	AllowedMethods        []string
	AllowedHeaders        []string
	ExposedHeaders        []string
	AllowCredentials      bool
	MaxAgeSeconds         int
}

type ContainerHubConfig struct {
	Enabled              bool
	BaseURL              string
	AuthToken            string
	DefaultEnvironmentID string
	RequestTimeoutMs     int
	DefaultSandboxLevel  string
	AgentIdleTimeoutMs   int64
	DestroyQueueDelayMs  int64
	ResolvedEngine       string
}

type DesktopConfig struct {
	Action DesktopBridgeConfig
	CDP    DesktopBridgeConfig
}

type DesktopBridgeConfig struct {
	Host             string
	Port             int
	Path             string
	RequestTimeoutMs int
	BridgeURL        string
}

type BashConfig struct {
	WorkingDirectory string
	// Deprecated: host path policy is loaded from AccessPolicy.
	AllowedPaths    []string
	AllowedCommands []string
	// Deprecated: host path policy is loaded from AccessPolicy.
	PathCheckedCommands []string
	// Deprecated: host path policy is loaded from AccessPolicy.
	PathCheckBypassCommands []string
	ShellFeaturesEnabled    bool
	ShellExecutable         string
	ShellArgs               []string
	ShellTimeoutMs          int
	MaxCommandChars         int
}

type FileToolsConfig struct {
	WorkingDirectory string
	// Deprecated: file path policy is loaded from AccessPolicy.
	AllowedReadPaths []string
	// Deprecated: file path policy is loaded from AccessPolicy.
	AllowedWritePaths      []string
	MaxReadBytes           int
	MaxWriteBytes          int
	MaxBatchOps            int
	RequireWriteApproval   bool
	RequireReadBeforeWrite bool
	Hooks                  FileToolsHooksConfig
}

type AccessPolicyConfig struct {
	Version          int
	WorkingDirectory string
	Levels           map[string]AccessPolicyLevelConfig
}

type AccessPolicyLevelConfig struct {
	Inherit       string
	ReadRoots     []string
	WriteRoots    []string
	ReadonlyRoots []string
	Approvals     AccessPolicyApprovalConfig
}

type AccessPolicyApprovalConfig struct {
	ReadOutsideRoots      string
	WriteOutsideRoots     string
	BashComplexFilesystem string
	BashOpaqueCommand     string
	BashWriteInWriteRoots string
}

type FileToolsHooksConfig struct {
	AfterFileChange FileAfterChangeHooksConfig
}

type FileAfterChangeHooksConfig struct {
	LSPDiagnostics LSPDiagnosticsHookConfig
}

type LSPDiagnosticsHookConfig struct {
	Enabled   bool
	TimeoutMs int
	Languages []string
	Servers   map[string]LSPServerConfig
}

type LSPServerConfig struct {
	Command string
	Args    []string
}

type BashHITLConfig struct {
	DefaultTimeoutMs int
}

type RunConfig struct {
	ReaperIntervalMs        int64
	MaxBackgroundDurationMs int64
	CompletedRetentionMs    int64
	EventBusMaxEvents       int
	MaxDisconnectedWaitMs   int64
	MaxObserversPerRun      int
}

type WebSocketConfig struct {
	MaxMessageSizeBytes int
	PingIntervalMs      int64
	WriteTimeoutMs      int64
	WriteQueueSize      int
	MaxObservesPerConn  int
}

type GatewayWSConfig struct {
	// URL 是完整的网关入口，包含 key / channel 等 query 参数，由 deploy 侧直接填写。
	// platform 不再二次拼接 query。
	URL string
	// JwtToken 是统一的鉴权凭据：既用于反向 WS 握手的 Authorization header，
	// 也用于 /api/push（artifact 外发）和 /api/pull（用户上传文件拉取）
	// 等 HTTP 旁路请求的 Bearer token。由用户在首次企微会话后从网关复制进 .env。
	JwtToken           string
	HandshakeTimeoutMs int64
	ReconnectMinMs     int64
	ReconnectMaxMs     int64
	// BaseURL 用于 artifact 外发和 userUpload 下载等 HTTP 旁路操作；
	// 未显式配置时从 URL 自动派生。
	BaseURL string

	// Gateways 支持多插件并存。每条 entry 独立反向连接、独立 reconnect。
	// entry 的 Channel 字段作为 chatId 前缀的路由键（chatId="wecom#..." → Channel="wecom"）。
	//
	// 兼容策略：部署侧只配置 legacy URL/JwtToken 时，normalize() 会把这条合成为
	// Gateways[0]（ID="default", Channel=""），路由层在"单条无 channel"场景下跳过前缀
	// 匹配，行为与未引入多 gateway 前字节一致。
}

// GatewayEntry 描述单个 gateway 反向连接条目。
type GatewayEntry struct {
	// ID 是 gateway 在 Registry 里的唯一键，Admin API 按 ID 增删。
	ID string
	// Channel 是用户配置的 channel id，用于 UI / 权限 / 管理面。
	// 兼容老部署时它也可以等于 chatId 前缀（如 "wecom"）。
	Channel string
	// SourceChannel 是 gateway URL 里 ?channel= 的完整来源标签（如 "wecom:xiaozhai"）。
	// SourcePrefix 是 SourceChannel 冒号前的来源前缀（如 "wecom"），用于兼容旧 chatId。
	SourceChannel      string
	SourcePrefix       string
	URL                string
	JwtToken           string
	BaseURL            string
	HandshakeTimeoutMs int64
	ReconnectMinMs     int64
	ReconnectMaxMs     int64
}

type ChannelType string

const (
	ChannelTypeBridge  ChannelType = "bridge"
	ChannelTypeGateway ChannelType = "gateway"
)

type ChannelConfig struct {
	ID           string
	Name         string
	Type         ChannelType
	DefaultAgent string
	Agents       []string
	AllAgents    bool
	Gateway      ChannelGatewayConfig
}

type ChannelGatewayConfig struct {
	URL                string
	JwtToken           string
	BaseURL            string
	HandshakeTimeoutMs int64
	ReconnectMinMs     int64
	ReconnectMaxMs     int64
}

// 网关 HTTP 旁路的路径约定，由网关侧固定，不再做成可配置。
const (
	GatewayUploadPath   = "/api/push"
	GatewayDownloadPath = "/api/pull"
)

func Load() (Config, error) {
	if err := checkDeprecatedEnvVars(); err != nil {
		return Config{}, err
	}
	cfg := defaultConfig()
	if err := cfg.applyStructuredConfig(); err != nil {
		return Config{}, err
	}
	cfg.applyEnv()
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(cfg.Server.Port) == "" {
		return Config{}, fmt.Errorf("SERVER_PORT must not be empty")
	}
	if strings.TrimSpace(cfg.Paths.RegistriesDir) == "" {
		return Config{}, fmt.Errorf("REGISTRIES_DIR must not be empty")
	}
	if err := validateExplicitDirEnv("PAN_DIR", cfg.Paths.PanDir); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func checkDeprecatedEnvVars() error {
	var found []string
	for _, key := range deprecatedEnvVars {
		if _, ok := os.LookupEnv(key); ok {
			found = append(found, key)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("deprecated environment variable(s) detected: %s; remove or migrate them before starting", strings.Join(found, ", "))
	}
	return nil
}

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
		GatewayWS: GatewayWSConfig{
			HandshakeTimeoutMs: 10000,
			ReconnectMinMs:     1000,
			ReconnectMaxMs:     30000,
		},
	}
}

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
				ReadRoots:     []string{"@workspace", "@agent", "@skills"},
				WriteRoots:    []string{"@workspace"},
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
					WriteOutsideRoots:     "auto",
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

func (c *Config) applyEnv() {
	c.Server.Port = stringEnv("SERVER_PORT", c.Server.Port)

	c.Paths.RegistriesDir = pathEnv("REGISTRIES_DIR", c.Paths.RegistriesDir)
	c.Paths.ToolsDir = pathEnv("TOOLS_DIR", c.Paths.ToolsDir)
	c.Paths.OwnerDir = pathEnv("OWNER_DIR", c.Paths.OwnerDir)
	c.Paths.AgentsDir = pathEnv("AGENTS_DIR", c.Paths.AgentsDir)
	c.Paths.TeamsDir = pathEnv("TEAMS_DIR", c.Paths.TeamsDir)
	c.Paths.RootDir = pathEnv("ROOT_DIR", c.Paths.RootDir)
	c.Paths.AutomationsDir = pathEnv("AUTOMATIONS_DIR", c.Paths.AutomationsDir)
	c.Paths.ChatsDir = pathEnv("CHATS_DIR", c.Paths.ChatsDir)
	c.Paths.MemoryDir = pathEnv("MEMORY_DIR", c.Paths.MemoryDir)
	c.Paths.PanDir = pathEnv("PAN_DIR", c.Paths.PanDir)
	c.Paths.SkillsMarketDir = pathEnv("SKILLS_MARKET_DIR", c.Paths.SkillsMarketDir)

	c.Agents.ExternalDir = pathEnv("AGENTS_DIR", c.Paths.AgentsDir)
	c.Teams.ExternalDir = pathEnv("TEAMS_DIR", c.Paths.TeamsDir)
	c.Skills.ExternalDir = pathEnv("SKILLS_MARKET_DIR", c.Paths.SkillsMarketDir)
	c.Skills.MaxPromptChars = intEnv("AGENT_SKILLS_MAX_PROMPT_CHARS", c.Skills.MaxPromptChars)
	c.Providers.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "providers"))
	c.Models.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "models"))

	c.Automation.ExternalDir = pathEnv("AUTOMATIONS_DIR", c.Paths.AutomationsDir)
	c.Automation.Enabled = boolEnv("AGENT_AUTOMATION_ENABLED", c.Automation.Enabled)
	c.Automation.DefaultZoneID = stringEnv("AGENT_AUTOMATION_DEFAULT_ZONE_ID", c.Automation.DefaultZoneID)
	c.Automation.PoolSize = intEnv("AGENT_AUTOMATION_POOL_SIZE", c.Automation.PoolSize)

	c.Memory.DBFileName = stringEnv("AGENT_MEMORY_DB_FILE_NAME", c.Memory.DBFileName)
	c.Memory.ContextTopN = intEnv("AGENT_MEMORY_CONTEXT_TOP_N", c.Memory.ContextTopN)
	c.Memory.ContextMaxChars = intEnv("AGENT_MEMORY_CONTEXT_MAX_CHARS", c.Memory.ContextMaxChars)
	c.Memory.SearchDefaultLimit = intEnv("AGENT_MEMORY_SEARCH_DEFAULT_LIMIT", c.Memory.SearchDefaultLimit)
	c.Memory.HybridVectorWeight = floatEnv("AGENT_MEMORY_HYBRID_VECTOR_WEIGHT", c.Memory.HybridVectorWeight)
	c.Memory.HybridFTSWeight = floatEnv("AGENT_MEMORY_HYBRID_FTS_WEIGHT", c.Memory.HybridFTSWeight)
	c.Memory.DualWriteMarkdown = boolEnv("AGENT_MEMORY_DUAL_WRITE_MARKDOWN", c.Memory.DualWriteMarkdown)
	c.Memory.StorageDir = pathEnv("MEMORY_DIR", c.Memory.StorageDir)

	c.Defaults.MaxTokens = intEnv("AGENT_DEFAULT_MAX_TOKENS", c.Defaults.MaxTokens)
	c.Defaults.Budget.RunTimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_RUN_TIMEOUT_MS", c.Defaults.Budget.RunTimeoutMs)
	c.Defaults.Budget.Model.MaxCalls = intEnv("AGENT_DEFAULT_BUDGET_MODEL_MAX_CALLS", c.Defaults.Budget.Model.MaxCalls)
	c.Defaults.Budget.Model.TimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_MODEL_TIMEOUT_MS", c.Defaults.Budget.Model.TimeoutMs)
	c.Defaults.Budget.Model.RetryCount = intEnv("AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT", c.Defaults.Budget.Model.RetryCount)
	c.Defaults.Budget.Tool.MaxCalls = intEnv("AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS", c.Defaults.Budget.Tool.MaxCalls)
	c.Defaults.Budget.Tool.TimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_TOOL_TIMEOUT_MS", c.Defaults.Budget.Tool.TimeoutMs)
	c.Defaults.Budget.Tool.RetryCount = intEnv("AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT", c.Defaults.Budget.Tool.RetryCount)
	c.Defaults.Budget.Hitl.TimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_HITL_TIMEOUT_MS", c.Defaults.Budget.Hitl.TimeoutMs)
	c.Defaults.React.MaxSteps = intEnv("AGENT_DEFAULT_REACT_MAX_STEPS", c.Defaults.React.MaxSteps)
	c.Defaults.Plan.MaxSteps = intEnv("AGENT_DEFAULT_PLAN_EXECUTE_MAX_STEPS", c.Defaults.Plan.MaxSteps)
	c.Defaults.Plan.MaxWorkRoundsPerTask = intEnv("AGENT_DEFAULT_PLAN_EXECUTE_MAX_WORK_ROUNDS_PER_TASK", c.Defaults.Plan.MaxWorkRoundsPerTask)

	c.Stream.IncludeToolPayloadEvents = boolEnv("STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS", c.Stream.IncludeToolPayloadEvents)
	c.Stream.DebugEventsEnabled = boolEnv("DEBUG_EVENTS_ENABLED", c.Stream.DebugEventsEnabled)
	c.Stream.DebugEventsEnabled = boolEnv("DELTA_LOGS_ENABLED", c.Stream.DebugEventsEnabled)
	c.SSE.HeartbeatIntervalMs = int64Env("AGENT_SSE_HEARTBEAT_INTERVAL_MS", c.SSE.HeartbeatIntervalMs)
	c.H2A.Render.FlushIntervalMs = int64Env("AGENT_H2A_RENDER_FLUSH_INTERVAL_MS", c.H2A.Render.FlushIntervalMs)
	c.H2A.Render.MaxBufferedChars = intEnv("AGENT_H2A_RENDER_MAX_BUFFERED_CHARS", c.H2A.Render.MaxBufferedChars)
	c.H2A.Render.MaxBufferedEvents = intEnv("AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS", c.H2A.Render.MaxBufferedEvents)
	c.H2A.Render.HeartbeatPassThrough = boolEnv("AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH", c.H2A.Render.HeartbeatPassThrough)

	c.Auth.Enabled = boolEnv("AUTH_ENABLED", c.Auth.Enabled)
	c.Auth.JWKSURI = stringEnv("AUTH_JWKS_URI", c.Auth.JWKSURI)
	c.Auth.Issuer = stringEnv("AUTH_ISSUER", c.Auth.Issuer)
	c.Auth.JWKSCacheSeconds = intEnv("AUTH_JWKS_CACHE_SECONDS", c.Auth.JWKSCacheSeconds)
	c.Auth.LocalPublicKeyFile = stringEnv("AUTH_LOCAL_PUBLIC_KEY_FILE", c.Auth.LocalPublicKeyFile)

	c.ResourceTicket.Secret = stringEnv("CHAT_RESOURCE_TICKET_SECRET", c.ResourceTicket.Secret)
	c.ResourceTicket.TTLSeconds = int64Env("CHAT_RESOURCE_TICKET_TTL_SECONDS", c.ResourceTicket.TTLSeconds)

	c.ChatStorage.Dir = pathEnv("CHATS_DIR", c.ChatStorage.Dir)
	c.ChatStorage.K = intEnv("CHAT_STORAGE_K", c.ChatStorage.K)
	c.ChatStorage.Charset = stringEnv("CHAT_STORAGE_CHARSET", c.ChatStorage.Charset)
	c.ChatStorage.ActionTools = csvEnv("CHAT_STORAGE_ACTION_TOOLS", c.ChatStorage.ActionTools)
	c.ChatStorage.IndexSQLiteFile = stringEnv("CHAT_STORAGE_INDEX_SQLITE_FILE", c.ChatStorage.IndexSQLiteFile)
	c.ChatStorage.IndexAutoRebuildOnIncompatibleSchema = boolEnv("CHAT_STORAGE_INDEX_AUTO_REBUILD_ON_INCOMPATIBLE_SCHEMA", c.ChatStorage.IndexAutoRebuildOnIncompatibleSchema)

	c.Logging.Request.Enabled = boolEnv("LOGGING_AGENT_REQUEST_ENABLED", c.Logging.Request.Enabled)
	c.Logging.Auth.Enabled = boolEnv("LOGGING_AGENT_AUTH_ENABLED", c.Logging.Auth.Enabled)
	c.Logging.Exception.Enabled = boolEnv("LOGGING_AGENT_EXCEPTION_ENABLED", c.Logging.Exception.Enabled)
	c.Logging.Tool.Enabled = boolEnv("LOGGING_AGENT_TOOL_ENABLED", c.Logging.Tool.Enabled)
	c.Logging.Action.Enabled = boolEnv("LOGGING_AGENT_ACTION_ENABLED", c.Logging.Action.Enabled)
	c.Logging.Viewport.Enabled = boolEnv("LOGGING_AGENT_VIEWPORT_ENABLED", c.Logging.Viewport.Enabled)
	c.Logging.SSE.Enabled = boolEnv("LOGGING_AGENT_SSE_ENABLED", c.Logging.SSE.Enabled)
	c.Logging.Memory.Enabled = boolEnv("LOGGING_MEMORY_ENABLED", c.Logging.Memory.Enabled)
	if strings.TrimSpace(c.Logging.Memory.File) == "" {
		c.Logging.Memory.File = memoryLogFileDefault(c.Paths.MemoryDir)
	}
	c.Logging.Memory.File = pathEnv("LOGGING_AGENT_MEMORY_FILE", c.Logging.Memory.File)
	c.Logging.LLMInteraction.Enabled = boolEnv("LOGGING_AGENT_LLM_INTERACTION_ENABLED", c.Logging.LLMInteraction.Enabled)
	c.Logging.LLMInteraction.MaskSensitive = boolEnv("LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE", c.Logging.LLMInteraction.MaskSensitive)

	c.ContainerHub.BaseURL = stringEnv("CONTAINER_HUB_BASE_URL", c.ContainerHub.BaseURL)
	c.ContainerHub.AuthToken = stringEnv("CONTAINER_HUB_AUTH_TOKEN", c.ContainerHub.AuthToken)
	c.ContainerHub.DefaultEnvironmentID = stringEnv("CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID", c.ContainerHub.DefaultEnvironmentID)
	c.ContainerHub.RequestTimeoutMs = intEnv("CONTAINER_HUB_REQUEST_TIMEOUT_MS", c.ContainerHub.RequestTimeoutMs)
	c.ContainerHub.DefaultSandboxLevel = strings.ToLower(stringEnv("CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL", c.ContainerHub.DefaultSandboxLevel))
	c.ContainerHub.AgentIdleTimeoutMs = int64Env("CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS", c.ContainerHub.AgentIdleTimeoutMs)
	c.ContainerHub.DestroyQueueDelayMs = int64Env("CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS", c.ContainerHub.DestroyQueueDelayMs)

	c.Run.ReaperIntervalMs = int64Env("AGENT_RUN_REAPER_INTERVAL_MS", c.Run.ReaperIntervalMs)
	c.Run.MaxBackgroundDurationMs = int64Env("AGENT_RUN_MAX_BACKGROUND_DURATION_MS", c.Run.MaxBackgroundDurationMs)
	c.Run.CompletedRetentionMs = int64Env("AGENT_RUN_COMPLETED_RETENTION_MS", c.Run.CompletedRetentionMs)
	c.Run.EventBusMaxEvents = intEnv("AGENT_RUN_EVENTBUS_MAX_EVENTS", c.Run.EventBusMaxEvents)
	c.Run.MaxDisconnectedWaitMs = int64Env("AGENT_RUN_MAX_DISCONNECTED_WAIT_MS", c.Run.MaxDisconnectedWaitMs)
	c.Run.MaxObserversPerRun = intEnv("AGENT_RUN_MAX_OBSERVERS_PER_RUN", c.Run.MaxObserversPerRun)
	c.WebSocket.MaxMessageSizeBytes = intEnv("AGENT_WS_MAX_MESSAGE_SIZE", c.WebSocket.MaxMessageSizeBytes)
	c.WebSocket.PingIntervalMs = int64Env("AGENT_WS_PING_INTERVAL_MS", c.WebSocket.PingIntervalMs)
	c.WebSocket.WriteTimeoutMs = int64Env("AGENT_WS_WRITE_TIMEOUT_MS", c.WebSocket.WriteTimeoutMs)
	c.WebSocket.WriteQueueSize = intEnv("AGENT_WS_WRITE_QUEUE_SIZE", c.WebSocket.WriteQueueSize)
	c.WebSocket.MaxObservesPerConn = intEnv("AGENT_WS_MAX_OBSERVES_PER_CONN", c.WebSocket.MaxObservesPerConn)
}

func memoryLogFileDefault(memoryDir string) string {
	if strings.TrimSpace(memoryDir) == "" {
		return ""
	}
	return filepath.Join(memoryDir, "memory.log")
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

// normalizeGateways 把 legacy 单 gateway 配置（GatewayWS）合成为 Gateways[0]。
// 已有 Gateways 列表时补缺省字段（ID、reconnect 参数），不覆盖已显式设置的值。
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
	if strings.TrimSpace(c.GatewayWS.URL) != "" {
		existingGatewayIDs["default"] = struct{}{}
		if legacyChannel := deriveChannelFromURL(c.GatewayWS.URL); legacyChannel != "" {
			existingGatewayChannels[legacyChannel] = struct{}{}
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
	if len(c.Gateways) == 0 && strings.TrimSpace(c.GatewayWS.URL) != "" {
		c.Gateways = append(c.Gateways, GatewayEntry{
			ID:                 "default",
			Channel:            deriveChannelFromURL(c.GatewayWS.URL),
			SourceChannel:      deriveSourceChannelFromURL(c.GatewayWS.URL),
			SourcePrefix:       deriveChannelFromURL(c.GatewayWS.URL),
			URL:                strings.TrimSpace(c.GatewayWS.URL),
			JwtToken:           strings.TrimSpace(c.GatewayWS.JwtToken),
			BaseURL:            strings.TrimSpace(c.GatewayWS.BaseURL),
			HandshakeTimeoutMs: c.GatewayWS.HandshakeTimeoutMs,
			ReconnectMinMs:     c.GatewayWS.ReconnectMinMs,
			ReconnectMaxMs:     c.GatewayWS.ReconnectMaxMs,
		})
	}
	for i := range c.Gateways {
		g := &c.Gateways[i]
		if strings.TrimSpace(g.ID) == "" {
			g.ID = fmt.Sprintf("gateway-%d", i)
		}
		if g.HandshakeTimeoutMs == 0 {
			g.HandshakeTimeoutMs = c.GatewayWS.HandshakeTimeoutMs
		}
		if g.ReconnectMinMs == 0 {
			g.ReconnectMinMs = c.GatewayWS.ReconnectMinMs
		}
		if g.ReconnectMaxMs == 0 {
			g.ReconnectMaxMs = c.GatewayWS.ReconnectMaxMs
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

func (c Config) ServerAddress() string {
	return ":" + c.Server.Port
}

func (c Config) IsLocalMode() bool {
	if !c.ContainerHub.Enabled {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.ContainerHub.ResolvedEngine), "local")
}

func resolveAuthLocalPublicKeyFile(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	clean := filepath.Clean(value)
	if clean == "." {
		return ""
	}
	if strings.Contains(filepath.ToSlash(clean), "/") {
		return ConfigFile(clean)
	}
	return ConfigFile(filepath.Join("configs", clean))
}

func stringEnv(key string, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return fallback
}

func pathEnv(key string, fallback string) string {
	value := stringEnv(key, fallback)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return filepath.Clean(value)
}

func validateExplicitDirEnv(key string, path string) error {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s does not exist: %s", key, path)
		}
		return fmt.Errorf("stat %s (%s): %w", key, path, err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", key, path)
	}
	return nil
}

func boolEnv(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return parseBool(strings.TrimSpace(raw), fallback)
}

func intEnv(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return parseInt(strings.TrimSpace(raw), fallback)
}

func int64Env(key string, fallback int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return int64(parseInt(strings.TrimSpace(raw), int(fallback)))
}

func floatEnv(key string, fallback float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	var parsed float64
	_, err := fmt.Sscanf(value, "%f", &parsed)
	if err != nil {
		return fallback
	}
	return parsed
}

func csvEnv(key string, fallback []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return splitCSV(raw)
}

func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]"))
	}
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.Trim(strings.TrimSpace(part), `"'`)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func parseBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var value int
	sign := 1
	for i, ch := range raw {
		if i == 0 && ch == '-' {
			sign = -1
			continue
		}
		if ch < '0' || ch > '9' {
			return fallback
		}
		value = value*10 + int(ch-'0')
	}
	return sign * value
}

func anyValue(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func stringValue(value any, fallback string) string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return fallback
	}
}

func boolValue(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return parseBool(v, fallback)
	default:
		return fallback
	}
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		return parseInt(v, fallback)
	default:
		return fallback
	}
}

func int64Value(value any, fallback int64) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		return int64(parseInt(v, int(fallback)))
	default:
		return fallback
	}
}

func listValue(value any, fallback []string) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			text := stringValue(item, "")
			if text != "" {
				items = append(items, text)
			}
		}
		return items
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return []string{strings.TrimSpace(v)}
	default:
		return fallback
	}
}

func csvOrList(value any, fallback []string) []string {
	switch v := value.(type) {
	case string:
		items := splitCSV(v)
		if len(items) == 0 {
			return fallback
		}
		return items
	case []any, []string:
		return listValue(v, fallback)
	default:
		return fallback
	}
}

var deprecatedEnvVars = []string{
	// Gateway 连接统一迁移到 configs/channels.yml。
	"GATEWAY_USER_ID",
	"GATEWAY_TICKET",
	"GATEWAY_AGENT_KEY",
	"GATEWAY_CHANNEL",
	"GATEWAY_UPLOAD_PATH",
	"GATEWAY_DOWNLOAD_PATH",
	"GATEWAY_AUTH_TOKEN",
	"GATEWAY_WS_URL",
	"AGENT_GATEWAY_WS_URL",
	"GATEWAY_JWT_TOKEN",
	"GATEWAY_BASE_URL",
	"AGENT_GATEWAY_WS_TOKEN",
	"AGENT_GATEWAY_WS_HANDSHAKE_TIMEOUT_MS",
	"AGENT_GATEWAY_WS_RECONNECT_MIN_MS",
	"AGENT_GATEWAY_WS_RECONNECT_MAX_MS",
	"AGENT_AUTH_ENABLED",
	"AGENT_AUTH_JWKS_URI",
	"AGENT_AUTH_ISSUER",
	"AGENT_AUTH_JWKS_CACHE_SECONDS",
	"AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE",
	"AGENT_CONTAINER_HUB_ENABLED",
	"AGENT_CONTAINER_HUB_BASE_URL",
	"AGENT_CONTAINER_HUB_AUTH_TOKEN",
	"AGENT_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID",
	"AGENT_CONTAINER_HUB_REQUEST_TIMEOUT_MS",
	"AGENT_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL",
	"AGENT_CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS",
	"AGENT_CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS",
	"AGENT_STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS",
	"AGENT_STREAM_INCLUDE_DEBUG_EVENTS",
	"STREAM_INCLUDE_DEBUG_EVENTS",
	"AGENT_BASH_WORKING_DIRECTORY",
	"AGENT_BASH_ALLOWED_PATHS",
	"AGENT_BASH_ALLOWED_COMMANDS",
	"AGENT_BASH_PATH_CHECKED_COMMANDS",
	"AGENT_BASH_PATH_CHECK_BYPASS_COMMANDS",
	"AGENT_BASH_SHELL_FEATURES_ENABLED",
	"AGENT_BASH_SHELL_EXECUTABLE",
	"AGENT_BASH_SHELL_ARGS",
	"AGENT_BASH_SHELL_TIMEOUT_MS",
	"AGENT_BASH_MAX_COMMAND_CHARS",
	"AGENT_BASH_HITL_DEFAULT_TIMEOUT_MS",
	"AGENT_FILE_WORKING_DIRECTORY",
	"AGENT_FILE_ALLOWED_READ_PATHS",
	"AGENT_FILE_ALLOWED_WRITE_PATHS",
	"AGENT_FILE_MAX_READ_BYTES",
	"AGENT_FILE_MAX_WRITE_BYTES",
	"AGENT_FILE_MAX_BATCH_OPS",
	"AGENT_FILE_REQUIRE_WRITE_APPROVAL",
	"AGENT_FILE_REQUIRE_READ_BEFORE_WRITE",
	"AGENT_CONFIG_DIR",
	"AGENT_AGENTS_EXTERNAL_DIR",
	"AGENT_TEAMS_EXTERNAL_DIR",
	"AGENT_MODELS_EXTERNAL_DIR",
	"AGENT_PROVIDERS_EXTERNAL_DIR",
	"AGENT_TOOLS_EXTERNAL_DIR",
	"AGENT_SKILLS_EXTERNAL_DIR",
	"AGENT_VIEWPORTS_EXTERNAL_DIR",
	"AGENT_MCP_SERVERS_REGISTRY_EXTERNAL_DIR",
	"AGENT_VIEWPORT_SERVERS_REGISTRY_EXTERNAL_DIR",
	"AGENT_AUTOMATION_EXTERNAL_DIR",
	"AGENT_DATA_EXTERNAL_DIR",
	"AGENT_MEMORY_STORAGE_DIR",
	"CHAT_IMAGE_TOKEN_TTL_SECONDS",
	"MEMORY_CHATS_DIR",
	"MEMORY_CHATS_K",
	"MEMORY_CHATS_CHARSET",
	"MEMORY_CHATS_ACTION_TOOLS",
	"MEMORY_CHATS_INDEX_SQLITE_FILE",
	"MEMORY_CHATS_INDEX_AUTO_REBUILD_ON_INCOMPATIBLE_SCHEMA",
}
