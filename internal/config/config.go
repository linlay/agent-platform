package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Server       ServerConfig
	Paths        PathsConfig
	Agents       CatalogConfig
	Teams        CatalogConfig
	Skills       SkillCatalogConfig
	Providers    CatalogConfig
	Models       CatalogConfig
	Schedule     ScheduleConfig
	Memory       MemoryConfig
	Defaults     DefaultsConfig
	SSE          SSEConfig
	H2A          H2AConfig
	Auth         AuthConfig
	ChatImage    ChatImageTokenConfig
	ChatStorage  ChatStorageConfig
	Logging      LoggingConfig
	CORS         CORSConfig
	ContainerHub ContainerHubConfig
	Bash         BashConfig
}

type ServerConfig struct {
	Port string
}

type PathsConfig struct {
	RegistriesDir   string
	OwnerDir        string
	AgentsDir       string
	TeamsDir        string
	RootDir         string
	SchedulesDir    string
	ChatsDir        string
	MemoryDir       string
	PanDir          string
	SkillsMarketDir string
}

type CatalogConfig struct {
	ExternalDir       string
	RefreshIntervalMs int64
}

type SkillCatalogConfig struct {
	CatalogConfig
	MaxPromptChars int
}

type ScheduleConfig struct {
	ExternalDir   string
	Enabled       bool
	DefaultZoneID string
	PoolSize      int
}

type MemoryConfig struct {
	DBFileName           string
	ContextTopN          int
	ContextMaxChars      int
	SearchDefaultLimit   int
	HybridVectorWeight   float64
	HybridFTSWeight      float64
	DualWriteMarkdown    bool
	EmbeddingProviderKey string
	EmbeddingModel       string
	EmbeddingDimension   int
	EmbeddingTimeoutMs   int
	StorageDir           string
	AutoRememberEnabled  bool
	RememberModelKey     string
	RememberTimeoutMs    int64
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
}

type RetryBudgetConfig struct {
	MaxCalls   int
	TimeoutMs  int
	RetryCount int
}

type ReactDefaultsConfig struct {
	MaxSteps int
}

type PlanExecuteDefaultsConfig struct {
	MaxSteps             int
	MaxWorkRoundsPerTask int
}

type SSEConfig struct {
	IncludeToolPayloadEvents bool
	HeartbeatIntervalMs      int64
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

type ChatImageTokenConfig struct {
	Secret                string
	TTLSeconds            int64
	ResourceTicketEnabled bool
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
	LLMInteraction LLMInteractionLoggingConfig
}

type ToggleConfig struct {
	Enabled bool
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
}

type BashConfig struct {
	WorkingDirectory        string
	AllowedPaths            []string
	AllowedCommands         []string
	PathCheckedCommands     []string
	PathCheckBypassCommands []string
	ShellFeaturesEnabled    bool
	ShellExecutable         string
	ShellTimeoutMs          int
	MaxCommandChars         int
}

func Load() (Config, error) {
	if err := rejectEnvVars(deprecatedEnvVars, "deprecated environment variables are not supported"); err != nil {
		return Config{}, err
	}

	cfg := defaultConfig()
	cfg.applyStructuredConfig()
	cfg.applyEnv()
	cfg.normalize()

	if strings.TrimSpace(cfg.Server.Port) == "" {
		return Config{}, fmt.Errorf("SERVER_PORT must not be empty")
	}
	if strings.TrimSpace(cfg.Paths.RegistriesDir) == "" {
		return Config{}, fmt.Errorf("REGISTRIES_DIR must not be empty")
	}
	return cfg, nil
}

func defaultConfig() Config {
	paths := PathsConfig{
		RegistriesDir:   filepath.Join("runtime", "registries"),
		OwnerDir:        filepath.Join("runtime", "owner"),
		AgentsDir:       filepath.Join("runtime", "agents"),
		TeamsDir:        filepath.Join("runtime", "teams"),
		RootDir:         filepath.Join("runtime", "root"),
		SchedulesDir:    filepath.Join("runtime", "schedules"),
		ChatsDir:        filepath.Join("runtime", "chats"),
		MemoryDir:       filepath.Join("runtime", "memory"),
		PanDir:          filepath.Join("runtime", "pan"),
		SkillsMarketDir: filepath.Join("runtime", "skills-market"),
	}
	return Config{
		Server: ServerConfig{Port: "8080"},
		Paths:  paths,
		Agents: CatalogConfig{ExternalDir: paths.AgentsDir, RefreshIntervalMs: 10000},
		Teams:  CatalogConfig{ExternalDir: paths.TeamsDir, RefreshIntervalMs: 30000},
		Skills: SkillCatalogConfig{
			CatalogConfig:  CatalogConfig{ExternalDir: paths.SkillsMarketDir, RefreshIntervalMs: 30000},
			MaxPromptChars: 8000,
		},
		Providers: CatalogConfig{
			ExternalDir:       filepath.Join(paths.RegistriesDir, "providers"),
			RefreshIntervalMs: 30000,
		},
		Models: CatalogConfig{
			ExternalDir:       filepath.Join(paths.RegistriesDir, "models"),
			RefreshIntervalMs: 30000,
		},
		Schedule: ScheduleConfig{
			ExternalDir: paths.SchedulesDir,
			Enabled:     true,
			PoolSize:    4,
		},
		Memory: MemoryConfig{
			DBFileName:          "memory.db",
			ContextTopN:         5,
			ContextMaxChars:     4000,
			SearchDefaultLimit:  10,
			HybridVectorWeight:  0.7,
			HybridFTSWeight:     0.3,
			DualWriteMarkdown:   true,
			EmbeddingDimension:  1024,
			EmbeddingTimeoutMs:  15000,
			StorageDir:          paths.MemoryDir,
			AutoRememberEnabled: false,
			RememberModelKey:    "",
			RememberTimeoutMs:   60000,
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
			},
			React: ReactDefaultsConfig{MaxSteps: 60},
			Plan: PlanExecuteDefaultsConfig{
				MaxSteps:             60,
				MaxWorkRoundsPerTask: 6,
			},
		},
		SSE: SSEConfig{
			IncludeToolPayloadEvents: false,
			HeartbeatIntervalMs:      15000,
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
		ChatImage: ChatImageTokenConfig{
			Secret:                "",
			TTLSeconds:            86400,
			ResourceTicketEnabled: true,
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
			LLMInteraction: LLMInteractionLoggingConfig{
				Enabled:       true,
				MaskSensitive: true,
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
			BaseURL:             "http://127.0.0.1:11960",
			RequestTimeoutMs:    300000,
			DefaultSandboxLevel: "run",
			AgentIdleTimeoutMs:  600000,
			DestroyQueueDelayMs: 5000,
		},
		Bash: BashConfig{
			WorkingDirectory:        "",
			AllowedPaths:            []string{".", "/tmp"},
			AllowedCommands:         []string{"ls", "pwd", "cat", "head", "tail", "top", "free", "df", "git", "rg", "find"},
			PathCheckedCommands:     []string{"ls", "cat", "head", "tail", "git", "rg", "find"},
			PathCheckBypassCommands: nil,
			ShellFeaturesEnabled:    false,
			ShellExecutable:         "bash",
			ShellTimeoutMs:          10000,
			MaxCommandChars:         16000,
		},
	}
}

func (c *Config) applyStructuredConfig() {
	c.applyContainerHubFile(ProjectFile("configs/container-hub.yml"))
	c.applyBashFile(ProjectFile("configs/bash.yml"))
	c.applyCORSFile(ProjectFile("configs/cors.yml"))
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
	c.ContainerHub.Enabled = boolValue(anyValue(values["enabled"], c.ContainerHub.Enabled), c.ContainerHub.Enabled)
	c.ContainerHub.BaseURL = stringValue(anyValue(values["base-url"], c.ContainerHub.BaseURL), c.ContainerHub.BaseURL)
	c.ContainerHub.AuthToken = stringValue(anyValue(values["auth-token"], c.ContainerHub.AuthToken), c.ContainerHub.AuthToken)
	c.ContainerHub.DefaultEnvironmentID = stringValue(anyValue(values["default-environment-id"], c.ContainerHub.DefaultEnvironmentID), c.ContainerHub.DefaultEnvironmentID)
	c.ContainerHub.RequestTimeoutMs = intValue(anyValue(values["request-timeout-ms"], c.ContainerHub.RequestTimeoutMs), c.ContainerHub.RequestTimeoutMs)
	c.ContainerHub.DefaultSandboxLevel = strings.ToLower(stringValue(anyValue(values["default-sandbox-level"], c.ContainerHub.DefaultSandboxLevel), c.ContainerHub.DefaultSandboxLevel))
	c.ContainerHub.AgentIdleTimeoutMs = int64Value(anyValue(values["agent-idle-timeout-ms"], c.ContainerHub.AgentIdleTimeoutMs), c.ContainerHub.AgentIdleTimeoutMs)
	c.ContainerHub.DestroyQueueDelayMs = int64Value(anyValue(values["destroy-queue-delay-ms"], c.ContainerHub.DestroyQueueDelayMs), c.ContainerHub.DestroyQueueDelayMs)
}

func (c *Config) applyBashFile(path string) {
	tree, err := LoadYAMLTree(path)
	if err != nil {
		return
	}
	values, _ := tree.(map[string]any)
	if len(values) == 0 {
		return
	}
	c.Bash.WorkingDirectory = stringValue(anyValue(values["working-directory"], c.Bash.WorkingDirectory), c.Bash.WorkingDirectory)
	c.Bash.AllowedPaths = csvOrList(anyValue(values["allowed-paths"], c.Bash.AllowedPaths), c.Bash.AllowedPaths)
	c.Bash.AllowedCommands = csvOrList(anyValue(values["allowed-commands"], c.Bash.AllowedCommands), c.Bash.AllowedCommands)
	c.Bash.PathCheckedCommands = csvOrList(anyValue(values["path-checked-commands"], c.Bash.PathCheckedCommands), c.Bash.PathCheckedCommands)
	c.Bash.PathCheckBypassCommands = csvOrList(anyValue(values["path-check-bypass-commands"], c.Bash.PathCheckBypassCommands), c.Bash.PathCheckBypassCommands)
	c.Bash.ShellFeaturesEnabled = boolValue(anyValue(values["shell-features-enabled"], c.Bash.ShellFeaturesEnabled), c.Bash.ShellFeaturesEnabled)
	c.Bash.ShellExecutable = stringValue(anyValue(values["shell-executable"], c.Bash.ShellExecutable), c.Bash.ShellExecutable)
	c.Bash.ShellTimeoutMs = intValue(anyValue(values["shell-timeout-ms"], c.Bash.ShellTimeoutMs), c.Bash.ShellTimeoutMs)
	c.Bash.MaxCommandChars = intValue(anyValue(values["max-command-chars"], c.Bash.MaxCommandChars), c.Bash.MaxCommandChars)
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

func (c *Config) applyEnv() {
	c.Server.Port = stringEnv("SERVER_PORT", c.Server.Port)

	c.Paths.RegistriesDir = pathEnv("REGISTRIES_DIR", c.Paths.RegistriesDir)
	c.Paths.OwnerDir = pathEnv("OWNER_DIR", c.Paths.OwnerDir)
	c.Paths.AgentsDir = pathEnv("AGENTS_DIR", c.Paths.AgentsDir)
	c.Paths.TeamsDir = pathEnv("TEAMS_DIR", c.Paths.TeamsDir)
	c.Paths.RootDir = pathEnv("ROOT_DIR", c.Paths.RootDir)
	c.Paths.SchedulesDir = pathEnv("SCHEDULES_DIR", c.Paths.SchedulesDir)
	c.Paths.ChatsDir = pathEnv("CHATS_DIR", c.Paths.ChatsDir)
	c.Paths.MemoryDir = pathEnv("MEMORY_DIR", c.Paths.MemoryDir)
	c.Paths.PanDir = pathEnv("PAN_DIR", c.Paths.PanDir)
	c.Paths.SkillsMarketDir = pathEnv("SKILLS_MARKET_DIR", c.Paths.SkillsMarketDir)

	c.Agents.ExternalDir = pathEnv("AGENTS_DIR", c.Paths.AgentsDir)
	c.Agents.RefreshIntervalMs = int64Env("AGENT_AGENTS_REFRESH_INTERVAL_MS", c.Agents.RefreshIntervalMs)
	c.Teams.ExternalDir = pathEnv("TEAMS_DIR", c.Paths.TeamsDir)
	c.Teams.RefreshIntervalMs = int64Env("AGENT_TEAMS_REFRESH_INTERVAL_MS", c.Teams.RefreshIntervalMs)
	c.Skills.ExternalDir = pathEnv("SKILLS_MARKET_DIR", c.Paths.SkillsMarketDir)
	c.Skills.RefreshIntervalMs = int64Env("AGENT_SKILLS_REFRESH_INTERVAL_MS", c.Skills.RefreshIntervalMs)
	c.Skills.MaxPromptChars = intEnv("AGENT_SKILLS_MAX_PROMPT_CHARS", c.Skills.MaxPromptChars)
	c.Providers.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "providers"))
	c.Providers.RefreshIntervalMs = int64Env("AGENT_PROVIDERS_REFRESH_INTERVAL_MS", c.Providers.RefreshIntervalMs)
	c.Models.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "models"))
	c.Models.RefreshIntervalMs = int64Env("AGENT_MODELS_REFRESH_INTERVAL_MS", c.Models.RefreshIntervalMs)

	c.Schedule.ExternalDir = pathEnv("SCHEDULES_DIR", c.Paths.SchedulesDir)
	c.Schedule.Enabled = boolEnv("AGENT_SCHEDULE_ENABLED", c.Schedule.Enabled)
	c.Schedule.DefaultZoneID = stringEnv("AGENT_SCHEDULE_DEFAULT_ZONE_ID", c.Schedule.DefaultZoneID)
	c.Schedule.PoolSize = intEnv("AGENT_SCHEDULE_POOL_SIZE", c.Schedule.PoolSize)

	c.Memory.DBFileName = stringEnv("AGENT_MEMORY_DB_FILE_NAME", c.Memory.DBFileName)
	c.Memory.ContextTopN = intEnv("AGENT_MEMORY_CONTEXT_TOP_N", c.Memory.ContextTopN)
	c.Memory.ContextMaxChars = intEnv("AGENT_MEMORY_CONTEXT_MAX_CHARS", c.Memory.ContextMaxChars)
	c.Memory.SearchDefaultLimit = intEnv("AGENT_MEMORY_SEARCH_DEFAULT_LIMIT", c.Memory.SearchDefaultLimit)
	c.Memory.HybridVectorWeight = floatEnv("AGENT_MEMORY_HYBRID_VECTOR_WEIGHT", c.Memory.HybridVectorWeight)
	c.Memory.HybridFTSWeight = floatEnv("AGENT_MEMORY_HYBRID_FTS_WEIGHT", c.Memory.HybridFTSWeight)
	c.Memory.DualWriteMarkdown = boolEnv("AGENT_MEMORY_DUAL_WRITE_MARKDOWN", c.Memory.DualWriteMarkdown)
	c.Memory.EmbeddingProviderKey = stringEnv("AGENT_MEMORY_EMBEDDING_PROVIDER_KEY", c.Memory.EmbeddingProviderKey)
	c.Memory.EmbeddingModel = stringEnv("AGENT_MEMORY_EMBEDDING_MODEL", c.Memory.EmbeddingModel)
	c.Memory.EmbeddingDimension = intEnv("AGENT_MEMORY_EMBEDDING_DIMENSION", c.Memory.EmbeddingDimension)
	c.Memory.EmbeddingTimeoutMs = intEnv("AGENT_MEMORY_EMBEDDING_TIMEOUT_MS", c.Memory.EmbeddingTimeoutMs)
	c.Memory.StorageDir = pathEnv("MEMORY_DIR", c.Memory.StorageDir)
	c.Memory.AutoRememberEnabled = boolEnv("AGENT_MEMORY_AUTO_REMEMBER_ENABLED", c.Memory.AutoRememberEnabled)
	c.Memory.RememberModelKey = stringEnv("AGENT_MEMORY_REMEMBER_MODEL_KEY", c.Memory.RememberModelKey)
	c.Memory.RememberTimeoutMs = int64Env("AGENT_MEMORY_REMEMBER_TIMEOUT_MS", c.Memory.RememberTimeoutMs)

	c.Defaults.MaxTokens = intEnv("AGENT_DEFAULT_MAX_TOKENS", c.Defaults.MaxTokens)
	c.Defaults.Budget.RunTimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_RUN_TIMEOUT_MS", c.Defaults.Budget.RunTimeoutMs)
	c.Defaults.Budget.Model.MaxCalls = intEnv("AGENT_DEFAULT_BUDGET_MODEL_MAX_CALLS", c.Defaults.Budget.Model.MaxCalls)
	c.Defaults.Budget.Model.TimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_MODEL_TIMEOUT_MS", c.Defaults.Budget.Model.TimeoutMs)
	c.Defaults.Budget.Model.RetryCount = intEnv("AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT", c.Defaults.Budget.Model.RetryCount)
	c.Defaults.Budget.Tool.MaxCalls = intEnv("AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS", c.Defaults.Budget.Tool.MaxCalls)
	c.Defaults.Budget.Tool.TimeoutMs = intEnv("AGENT_DEFAULT_BUDGET_TOOL_TIMEOUT_MS", c.Defaults.Budget.Tool.TimeoutMs)
	c.Defaults.Budget.Tool.RetryCount = intEnv("AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT", c.Defaults.Budget.Tool.RetryCount)
	c.Defaults.React.MaxSteps = intEnv("AGENT_DEFAULT_REACT_MAX_STEPS", c.Defaults.React.MaxSteps)
	c.Defaults.Plan.MaxSteps = intEnv("AGENT_DEFAULT_PLAN_EXECUTE_MAX_STEPS", c.Defaults.Plan.MaxSteps)
	c.Defaults.Plan.MaxWorkRoundsPerTask = intEnv("AGENT_DEFAULT_PLAN_EXECUTE_MAX_WORK_ROUNDS_PER_TASK", c.Defaults.Plan.MaxWorkRoundsPerTask)

	c.SSE.IncludeToolPayloadEvents = boolEnv("AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS", c.SSE.IncludeToolPayloadEvents)
	c.SSE.HeartbeatIntervalMs = int64Env("AGENT_SSE_HEARTBEAT_INTERVAL_MS", c.SSE.HeartbeatIntervalMs)
	c.H2A.Render.FlushIntervalMs = int64Env("AGENT_H2A_RENDER_FLUSH_INTERVAL_MS", c.H2A.Render.FlushIntervalMs)
	c.H2A.Render.MaxBufferedChars = intEnv("AGENT_H2A_RENDER_MAX_BUFFERED_CHARS", c.H2A.Render.MaxBufferedChars)
	c.H2A.Render.MaxBufferedEvents = intEnv("AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS", c.H2A.Render.MaxBufferedEvents)
	c.H2A.Render.HeartbeatPassThrough = boolEnv("AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH", c.H2A.Render.HeartbeatPassThrough)

	c.Auth.Enabled = boolEnv("AGENT_AUTH_ENABLED", c.Auth.Enabled)
	c.Auth.JWKSURI = stringEnv("AGENT_AUTH_JWKS_URI", c.Auth.JWKSURI)
	c.Auth.Issuer = stringEnv("AGENT_AUTH_ISSUER", c.Auth.Issuer)
	c.Auth.JWKSCacheSeconds = intEnv("AGENT_AUTH_JWKS_CACHE_SECONDS", c.Auth.JWKSCacheSeconds)
	c.Auth.LocalPublicKeyFile = stringEnv("AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE", c.Auth.LocalPublicKeyFile)

	c.ChatImage.Secret = stringEnv("CHAT_IMAGE_TOKEN_SECRET", c.ChatImage.Secret)
	c.ChatImage.TTLSeconds = int64Env("CHAT_IMAGE_TOKEN_TTL_SECONDS", c.ChatImage.TTLSeconds)
	c.ChatImage.ResourceTicketEnabled = boolEnv("CHAT_RESOURCE_TICKET_ENABLED", c.ChatImage.ResourceTicketEnabled)

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
	c.Logging.LLMInteraction.Enabled = boolEnv("LOGGING_AGENT_LLM_INTERACTION_ENABLED", c.Logging.LLMInteraction.Enabled)
	c.Logging.LLMInteraction.MaskSensitive = boolEnv("LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE", c.Logging.LLMInteraction.MaskSensitive)

	c.ContainerHub.Enabled = boolEnv("AGENT_CONTAINER_HUB_ENABLED", c.ContainerHub.Enabled)
	c.ContainerHub.BaseURL = stringEnv("AGENT_CONTAINER_HUB_BASE_URL", c.ContainerHub.BaseURL)
	c.ContainerHub.AuthToken = stringEnv("AGENT_CONTAINER_HUB_AUTH_TOKEN", c.ContainerHub.AuthToken)
	c.ContainerHub.DefaultEnvironmentID = stringEnv("AGENT_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID", c.ContainerHub.DefaultEnvironmentID)
	c.ContainerHub.RequestTimeoutMs = intEnv("AGENT_CONTAINER_HUB_REQUEST_TIMEOUT_MS", c.ContainerHub.RequestTimeoutMs)
	c.ContainerHub.DefaultSandboxLevel = strings.ToLower(stringEnv("AGENT_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL", c.ContainerHub.DefaultSandboxLevel))
	c.ContainerHub.AgentIdleTimeoutMs = int64Env("AGENT_CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS", c.ContainerHub.AgentIdleTimeoutMs)
	c.ContainerHub.DestroyQueueDelayMs = int64Env("AGENT_CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS", c.ContainerHub.DestroyQueueDelayMs)

	c.Bash.WorkingDirectory = pathEnv("AGENT_BASH_WORKING_DIRECTORY", c.Bash.WorkingDirectory)
	c.Bash.AllowedPaths = csvEnv("AGENT_BASH_ALLOWED_PATHS", c.Bash.AllowedPaths)
	c.Bash.AllowedCommands = csvEnv("AGENT_BASH_ALLOWED_COMMANDS", c.Bash.AllowedCommands)
	c.Bash.PathCheckedCommands = csvEnv("AGENT_BASH_PATH_CHECKED_COMMANDS", c.Bash.PathCheckedCommands)
	c.Bash.PathCheckBypassCommands = csvEnv("AGENT_BASH_PATH_CHECK_BYPASS_COMMANDS", c.Bash.PathCheckBypassCommands)
	c.Bash.ShellFeaturesEnabled = boolEnv("AGENT_BASH_SHELL_FEATURES_ENABLED", c.Bash.ShellFeaturesEnabled)
	c.Bash.ShellExecutable = stringEnv("AGENT_BASH_SHELL_EXECUTABLE", c.Bash.ShellExecutable)
	c.Bash.ShellTimeoutMs = intEnv("AGENT_BASH_SHELL_TIMEOUT_MS", c.Bash.ShellTimeoutMs)
	c.Bash.MaxCommandChars = intEnv("AGENT_BASH_MAX_COMMAND_CHARS", c.Bash.MaxCommandChars)
}

func (c *Config) normalize() {
	c.Paths.RegistriesDir = filepath.Clean(c.Paths.RegistriesDir)
	c.Paths.OwnerDir = filepath.Clean(c.Paths.OwnerDir)
	c.Paths.AgentsDir = filepath.Clean(c.Paths.AgentsDir)
	c.Paths.TeamsDir = filepath.Clean(c.Paths.TeamsDir)
	c.Paths.RootDir = filepath.Clean(c.Paths.RootDir)
	c.Paths.SchedulesDir = filepath.Clean(c.Paths.SchedulesDir)
	c.Paths.ChatsDir = filepath.Clean(c.Paths.ChatsDir)
	c.Paths.MemoryDir = filepath.Clean(c.Paths.MemoryDir)
	c.Paths.PanDir = filepath.Clean(c.Paths.PanDir)
	c.Paths.SkillsMarketDir = filepath.Clean(c.Paths.SkillsMarketDir)

	c.Agents.ExternalDir = filepath.Clean(c.Paths.AgentsDir)
	c.Teams.ExternalDir = filepath.Clean(c.Paths.TeamsDir)
	c.Skills.ExternalDir = filepath.Clean(c.Paths.SkillsMarketDir)
	c.Schedule.ExternalDir = filepath.Clean(c.Paths.SchedulesDir)
	c.Memory.StorageDir = filepath.Clean(c.Paths.MemoryDir)
	c.ChatStorage.Dir = filepath.Clean(c.Paths.ChatsDir)
	c.Providers.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "providers"))
	c.Models.ExternalDir = filepath.Clean(filepath.Join(c.Paths.RegistriesDir, "models"))

	c.Auth.LocalPublicKeyFile = resolveAuthLocalPublicKeyFile(c.Auth.LocalPublicKeyFile)
	if c.ContainerHub.DefaultSandboxLevel == "" {
		c.ContainerHub.DefaultSandboxLevel = "run"
	}
	if c.Bash.WorkingDirectory == "" {
		c.Bash.WorkingDirectory = "."
	}
}

func (c Config) ServerAddress() string {
	return ":" + c.Server.Port
}

func rejectEnvVars(keys []string, message string) error {
	var found []string
	for _, key := range keys {
		if _, ok := os.LookupEnv(key); ok {
			found = append(found, key)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("%s: %s", message, strings.Join(found, ", "))
	}
	return nil
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
		return ProjectFile(clean)
	}
	return ProjectFile(filepath.Join("configs", clean))
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
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
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
	"RUNTIME_DIR",
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
	"AGENT_SCHEDULE_EXTERNAL_DIR",
	"AGENT_DATA_EXTERNAL_DIR",
	"AGENT_MEMORY_STORAGE_DIR",
	"MEMORY_CHATS_DIR",
	"MEMORY_CHATS_K",
	"MEMORY_CHATS_CHARSET",
	"MEMORY_CHATS_ACTION_TOOLS",
	"MEMORY_CHATS_INDEX_SQLITE_FILE",
	"MEMORY_CHATS_INDEX_AUTO_REBUILD_ON_INCOMPATIBLE_SCHEMA",
}
