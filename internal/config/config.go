package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Server          ServerConfig
	Paths           PathsConfig
	Agents          CatalogConfig
	Teams           CatalogConfig
	Skills          SkillCatalogConfig
	Prompts         PromptsConfig
	CoderPrompts    CoderPromptsConfig
	KBasePrompts    KBasePromptsConfig
	MemoryPrompts   MemoryPromptsConfig
	CoderSettings   CoderSettingsConfig
	KBase           KBaseConfig
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
	KBaseDir        string
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
	BTW          BTWPromptsConfig
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

// BTWPromptsConfig controls the user-level instructions used by hidden side
// questions. These prompts never contribute to the system-init cache key.
type BTWPromptsConfig struct {
	UserPromptTemplate string
	FinalAnswerPrompt  string
}

type CoderPromptsConfig struct {
	SystemPrompt   string
	PlanningPrompt string
}

type KBasePromptsConfig struct {
	SystemPrompt string
}

type MemoryPromptsConfig struct {
	SystemPromptTemplate string
	UserPromptTemplate   string
}

type CoderSettingsConfig struct {
	WorkspaceAgents CoderWorkspaceAgentsConfig
	DefaultAgent    CoderDefaultAgentConfig
	ACPBridges      map[string]CoderACPBridgeConfig
}

type CoderWorkspaceAgentsConfig struct {
	Enabled bool
	File    string
}

type CoderDefaultAgentConfig struct {
	ModelKey        string
	ReasoningEffort string
	Budget          map[string]any
}

type CoderACPBridgeConfig struct {
	BaseURL   string
	AuthToken string
	TimeoutMS int
}

type KBaseConfig struct {
	DefaultAgent KBaseDefaultAgentConfig
	Embedding    KBaseEmbeddingConfig
	Index        KBaseIndexConfig
	Maintenance  KBaseMaintenanceConfig
	Refresh      KBaseRefreshConfig
	Extraction   KBaseExtractionConfig
}

type KBaseDefaultAgentConfig struct {
	ModelKey        string
	ReasoningEffort string
}

type KBaseEmbeddingConfig struct {
	ModelKey string
}

type KBaseIndexConfig struct {
	FTS    KBaseFTSIndexConfig
	Vector KBaseVectorIndexConfig
}

type KBaseFTSIndexConfig struct {
	BaseTokenizer string
}

type KBaseVectorIndexConfig struct {
	ANNMinRows int
}

type KBaseMaintenanceConfig struct {
	OptimizeChangeThreshold int
	OptimizeInterval        time.Duration
	VersionRetention        time.Duration
}

type KBaseRefreshConfig struct {
	Debounce          time.Duration
	ReconcileInterval time.Duration
}

type KBaseExtractionConfig struct {
	Timeout      time.Duration
	MaxFileBytes int64
	PDF          KBasePDFExtractionConfig
	DOCX         KBaseDOCXExtractionConfig
	PPTX         KBasePPTXExtractionConfig
}

type KBasePDFExtractionConfig struct {
	Enabled bool
	Backend string
	Binary  string
}

type KBaseDOCXExtractionConfig struct {
	Enabled bool
	Backend string
}

type KBasePPTXExtractionConfig struct {
	Enabled      bool
	Backend      string
	IncludeNotes bool
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
	Budget        BudgetDefaultsConfig
	React         ReactDefaultsConfig
	Plan          PlanExecuteDefaultsConfig
	CoderPlanning CoderPlanningDefaultsConfig
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

type CoderPlanningDefaultsConfig struct {
	MaxSteps int
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
	WorkingDirectory     string
	AllowedCommands      []string
	ShellFeaturesEnabled bool
	ShellExecutable      string
	ShellArgs            []string
	MaxCommandChars      int
}

type FileToolsConfig struct {
	WorkingDirectory       string
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

type ChannelMode string

const (
	ChannelModeClient ChannelMode = "client"
	ChannelModeServer ChannelMode = "server"
)

const (
	ChannelTransportWebSocket = "websocket"
	ChannelProtocolPlatformWS = "platform-ws"
)

type ChannelConfig struct {
	ID           string
	Name         string
	Type         ChannelType
	Mode         ChannelMode
	Transport    string
	Protocol     string
	DefaultAgent string
	Agents       []string
	AllAgents    bool
	Endpoint     ChannelEndpointConfig
	Auth         ChannelAuthConfig
	Heartbeat    ChannelHeartbeatConfig
	Reconnect    ChannelReconnectConfig
	Gateway      ChannelGatewayConfig
}

type ChannelEndpointConfig struct {
	URL      string
	Path     string
	Token    string
	TokenEnv string
}

type ChannelAuthConfig struct {
	Type string
}

type ChannelHeartbeatConfig struct {
	Interval int64 // seconds
}

type ChannelReconnectConfig struct {
	HandshakeTimeout int64 // seconds
	Min              int64 // seconds
	Max              int64 // seconds
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
