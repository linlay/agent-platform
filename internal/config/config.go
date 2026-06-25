package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Config struct {
	Server          ServerConfig
	Paths           PathsConfig
	Agents          CatalogConfig
	Teams           CatalogConfig
	Skills          SkillCatalogConfig
	Prompts         PromptsConfig
	CoderPrompts    CoderPromptsConfig
	MemoryPrompts   MemoryPromptsConfig
	CoderSettings   CoderSettingsConfig
	VisionRecognize VisionRecognizeConfig
	WebFetch        WebFetchConfig
	ImageGenerate   ImageGenerateConfig
	Providers       CatalogConfig
	Models          CatalogConfig
	Query           QueryConfig
	Automation      AutomationConfig
	Billing         BillingConfig
	Memory          MemoryConfig
	Defaults        DefaultsConfig
	SSE             SSEConfig
	Auth            AuthConfig
	ResourceTicket  ResourceTicketConfig
	Logging         LoggingConfig
	CORS            CORSConfig
	ContainerHub    ContainerHubConfig
	Desktop         DesktopConfig
	AccessPolicy    AccessPolicyConfig
	Bash            BashConfig
	SandboxBash     SandboxBashConfig
	FileTools       FileToolsConfig
	Run             RunConfig
	WebSocket       WebSocketConfig
	// Gateways 是多 gateway 反向连接列表（wecom / feishu / ding / ...）。
	Gateways []GatewayEntry
	// Channels 是 channel 元数据与 agent 准入配置；每条可合成一条 gateway entry。
	Channels []ChannelConfig
}

type LoadOptions struct {
	ConfigDir string
	Port      string
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

type QueryConfig struct {
	AdvancedUserPrompt bool
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
	SystemPrompt              string
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
	DefaultAgent    CoderDefaultAgentConfig
	ACPProxies      map[string]CoderACPProxyConfig
}

type CoderWorkspaceAgentsConfig struct {
	Enabled bool
	File    string
}

type CoderDefaultAgentConfig struct {
	ModelKey        string
	ReasoningEffort string
}

type CoderACPProxyConfig struct {
	BaseURL   string
	AuthToken string
	Timeout   int // seconds
}

type VisionRecognizeConfig struct {
	Enabled        bool
	DefaultProfile string
	Profiles       map[string]VisionRecognizeProfileConfig
}

type VisionRecognizeProfileConfig struct {
	ModelKey      string
	Timeout       int // seconds
	MaxImages     int
	MaxImageBytes int
	OutputFormat  string
	SystemPrompt  string
}

type WebFetchConfig struct {
	Enabled          bool
	DefaultProfile   string
	PreapprovedHosts []string
	Profiles         map[string]WebFetchProfileConfig
}

type WebFetchProfileConfig struct {
	ModelKey         string
	Timeout          int // seconds
	FetchTimeout     int // seconds
	MaxURLLength     int
	MaxResponseBytes int
	MaxMarkdownChars int
	MaxOutputTokens  int
	SystemPrompt     string
}

type ImageGenerateConfig struct {
	Enabled        bool
	DefaultProfile string
	Profiles       map[string]ImageGenerateProfileConfig
}

type ImageGenerateProfileConfig struct {
	ModelKey        string
	Timeout         int // seconds
	Size            string
	ResponseFormat  string
	OutputMimeType  string
	MaxPromptChars  int
	PersistArtifact bool
	EndpointPath    string
}

type AutomationConfig struct {
	ExternalDir   string
	Enabled       bool
	DefaultZoneID string
	PoolSize      int
}

type BillingConfig struct {
	Currency string
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
	Budget BudgetDefaultsConfig
	React  ReactDefaultsConfig
	Plan   PlanExecuteDefaultsConfig
}

type BudgetDefaultsConfig struct {
	Timeout  int // seconds
	MaxSteps int
	Model    RetryBudgetConfig
	Tool     RetryBudgetConfig
	Hitl     HitlBudgetConfig
	Stages   map[string]StageBudgetConfig
}

type RetryBudgetConfig struct {
	MaxCalls   int
	Timeout    int // seconds
	RetryCount int
}

type StageBudgetConfig struct {
	MaxSteps int
	Tool     RetryBudgetConfig
}

type HitlBudgetConfig struct {
	Timeout  int // seconds
	Question HitlModeBudgetConfig
	Approval HitlModeBudgetConfig
	Form     HitlModeBudgetConfig
	Plan     HitlModeBudgetConfig
}

type HitlModeBudgetConfig struct {
	Timeout int // seconds
}

type ReactDefaultsConfig struct {
	MaxSteps int
}

type PlanExecuteDefaultsConfig struct {
	MaxSteps             int
	MaxWorkRoundsPerTask int
}

type SSEConfig struct {
	HeartbeatInterval int64 // seconds
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
	Enabled           bool
	ConsoleCategories []string
	MaskSensitive     bool
	RecordEnabled     bool
	RecordDir         string
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
	RequestTimeout       int // 秒
	DefaultSandboxLevel  string
	AgentIdleTimeout     int64 // 秒
	DestroyQueueDelay    int64 // 秒
	ResolvedEngine       string
}

type DesktopConfig struct {
	Action DesktopBridgeConfig
	CDP    DesktopBridgeConfig
}

type DesktopBridgeConfig struct {
	Host           string
	Port           int
	Path           string
	RequestTimeout int // seconds
	BridgeURL      string
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
	ShellTimeout            int // seconds
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
	ReadBeforeWriteScope   string
	Hooks                  FileToolsHooksConfig
}

type AccessPolicyConfig struct {
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

type SandboxBashConfig struct {
	Security SandboxBashSecurityConfig
}

type SandboxBashSecurityConfig struct {
	BashsecOverrides   SandboxBashBashsecOverridesConfig
	AuditAutoApprovals bool
}

type SandboxBashBashsecOverridesConfig struct {
	OutputRedirection        string
	HeredocOutputRedirection string
}

type FileToolsHooksConfig struct {
	AfterFileChange FileAfterChangeHooksConfig
}

type FileAfterChangeHooksConfig struct {
	LSPDiagnostics LSPDiagnosticsHookConfig
}

type LSPDiagnosticsHookConfig struct {
	Enabled   bool
	Timeout   int // seconds
	Languages []string
	Servers   map[string]LSPServerConfig
}

type LSPServerConfig struct {
	Command string
	Args    []string
}

type RunConfig struct {
	ReaperInterval        int64 // seconds
	MaxBackgroundDuration int64 // seconds
	CompletedRetention    int64 // seconds
	EventBusMaxEvents     int
	MaxDisconnectedWait   int64 // seconds
	MaxObserversPerRun    int
}

type WebSocketConfig struct {
	MaxMessageSizeBytes int
	PingInterval        int64 // seconds
	WriteTimeout        int64 // seconds
	WriteQueueSize      int
	MaxObservesPerConn  int
}

const (
	defaultGatewayHandshakeTimeout int64 = 10 // seconds
	defaultGatewayReconnectMin     int64 = 1  // seconds
	defaultGatewayReconnectMax     int64 = 30 // seconds
)

// GatewayEntry 描述单个 gateway 反向连接条目。
type GatewayEntry struct {
	// ID 是 gateway 在 Registry 里的唯一键，Admin API 按 ID 增删。
	ID string
	// Channel 是用户配置的 channel id，用于 UI / 权限 / 管理面。
	Channel string
	// SourceChannel 是 gateway URL 里 ?channel= 的完整来源标签（如 "wecom:xiaozhai"）。
	// SourcePrefix 是 SourceChannel 冒号前的来源前缀（如 "wecom"）。
	SourceChannel    string
	SourcePrefix     string
	URL              string
	JwtToken         string
	BaseURL          string
	HandshakeTimeout int64 // seconds
	ReconnectMin     int64 // seconds
	ReconnectMax     int64 // seconds
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
	URL              string
	JwtToken         string
	BaseURL          string
	HandshakeTimeout int64 // seconds
	ReconnectMin     int64 // seconds
	ReconnectMax     int64 // seconds
}

// 网关 HTTP 旁路的路径约定，由网关侧固定，不再做成可配置。
const (
	GatewayUploadPath   = "/api/push"
	GatewayDownloadPath = "/api/pull"
)

func Load(optionValues ...LoadOptions) (Config, error) {
	options := LoadOptions{}
	if len(optionValues) > 0 {
		options = optionValues[0]
	}
	options.ConfigDir = resolveConfigRoot(options.ConfigDir)
	options.Port = strings.TrimSpace(options.Port)

	cfg := defaultConfig(options)
	if err := cfg.applyStructuredConfig(options.ConfigDir); err != nil {
		return Config{}, err
	}
	cfg.applyEnv(options)
	if options.Port != "" {
		cfg.Server.Port = options.Port
	}
	if err := cfg.normalize(options.ConfigDir); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(cfg.Server.Port) == "" {
		return Config{}, fmt.Errorf("SERVER_PORT must not be empty")
	}
	if strings.TrimSpace(cfg.Paths.RegistriesDir) == "" {
		return Config{}, fmt.Errorf("AP_RUNTIME_REGISTRIES_DIR must not be empty")
	}
	if err := validateExplicitDirEnv("AP_RUNTIME_PAN_DIR", cfg.Paths.PanDir); err != nil {
		return Config{}, err
	}
	return cfg, nil
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

func fixedAuthLocalPublicKeyFile(configRoot string) string {
	return configFile(configRoot, filepath.Join("configs", "local-public-key.pem"))
}
