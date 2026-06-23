package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (c *Config) applyEnv(options LoadOptions) {
	if strings.TrimSpace(options.Port) == "" {
		c.Server.Port = stringEnv("SERVER_PORT", c.Server.Port)
	}

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

	c.Billing.Currency = strings.ToUpper(stringEnv("BILLING_CURRENCY", c.Billing.Currency))

	c.Memory.DBFileName = stringEnv("AGENT_MEMORY_DB_FILE_NAME", c.Memory.DBFileName)
	c.Memory.ContextTopN = intEnv("AGENT_MEMORY_CONTEXT_TOP_N", c.Memory.ContextTopN)
	c.Memory.ContextMaxChars = intEnv("AGENT_MEMORY_CONTEXT_MAX_CHARS", c.Memory.ContextMaxChars)
	c.Memory.SearchDefaultLimit = intEnv("AGENT_MEMORY_SEARCH_DEFAULT_LIMIT", c.Memory.SearchDefaultLimit)
	c.Memory.HybridVectorWeight = floatEnv("AGENT_MEMORY_HYBRID_VECTOR_WEIGHT", c.Memory.HybridVectorWeight)
	c.Memory.HybridFTSWeight = floatEnv("AGENT_MEMORY_HYBRID_FTS_WEIGHT", c.Memory.HybridFTSWeight)
	c.Memory.DualWriteMarkdown = boolEnv("AGENT_MEMORY_DUAL_WRITE_MARKDOWN", c.Memory.DualWriteMarkdown)
	c.Memory.StorageDir = pathEnv("MEMORY_DIR", c.Memory.StorageDir)

	c.Defaults.MaxOutputTokens = intEnv("AGENT_DEFAULT_MAX_OUTPUT_TOKENS", c.Defaults.MaxOutputTokens)
	_, defaultBudgetMaxStepsEnv := os.LookupEnv("AGENT_DEFAULT_BUDGET_MAX_STEPS")
	_, defaultToolMaxCallsEnv := os.LookupEnv("AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS")
	c.Defaults.Budget.MaxSteps = intEnv("AGENT_DEFAULT_BUDGET_MAX_STEPS", c.Defaults.Budget.MaxSteps)
	c.Defaults.Budget.Model.RetryCount = intEnv("AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT", c.Defaults.Budget.Model.RetryCount)
	c.Defaults.Budget.Tool.MaxCalls = intEnv("AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS", c.Defaults.Budget.Tool.MaxCalls)
	if defaultBudgetMaxStepsEnv && !defaultToolMaxCallsEnv && c.Defaults.Budget.MaxSteps > 0 {
		c.Defaults.Budget.Tool.MaxCalls = c.Defaults.Budget.MaxSteps * 2
	}
	c.Defaults.Budget.Tool.RetryCount = intEnv("AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT", c.Defaults.Budget.Tool.RetryCount)
	c.Defaults.Budget.Hitl.Timeout = intEnv("BUDGET_HITL_TIMEOUT", c.Defaults.Budget.Hitl.Timeout)
	c.Defaults.Budget.Hitl.Question.Timeout = intEnv("BUDGET_HITL_QUESTION_TIMEOUT", c.Defaults.Budget.Hitl.Question.Timeout)
	c.Defaults.Budget.Hitl.Approval.Timeout = intEnv("BUDGET_HITL_APPROVAL_TIMEOUT", c.Defaults.Budget.Hitl.Approval.Timeout)
	c.Defaults.Budget.Hitl.Form.Timeout = intEnv("BUDGET_HITL_FORM_TIMEOUT", c.Defaults.Budget.Hitl.Form.Timeout)
	c.Defaults.Budget.Hitl.Plan.Timeout = intEnv("BUDGET_HITL_PLAN_TIMEOUT", c.Defaults.Budget.Hitl.Plan.Timeout)
	c.Stream.IncludeToolPayloadEvents = boolEnvPair("AP_STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS", "STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS", c.Stream.IncludeToolPayloadEvents)
	c.H2A.Render.FlushInterval = int64Env("AGENT_H2A_RENDER_FLUSH_INTERVAL", c.H2A.Render.FlushInterval)
	c.H2A.Render.MaxBufferedChars = intEnv("AGENT_H2A_RENDER_MAX_BUFFERED_CHARS", c.H2A.Render.MaxBufferedChars)
	c.H2A.Render.MaxBufferedEvents = intEnv("AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS", c.H2A.Render.MaxBufferedEvents)
	c.H2A.Render.HeartbeatPassThrough = boolEnv("AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH", c.H2A.Render.HeartbeatPassThrough)
	c.I18N.DefaultLocale = stringEnv("I18N_DEFAULT_LOCALE", c.I18N.DefaultLocale)

	c.Auth.Enabled = boolEnvPair("AP_AUTH_ENABLED", "AUTH_ENABLED", c.Auth.Enabled)
	c.Auth.JWKSURI = stringEnvPair("AP_AUTH_JWKS_URI", "AUTH_JWKS_URI", c.Auth.JWKSURI)
	c.Auth.Issuer = stringEnvPair("AP_AUTH_ISSUER", "AUTH_ISSUER", c.Auth.Issuer)
	c.Auth.JWKSCacheSeconds = intEnvPair("AP_AUTH_JWKS_CACHE_SECONDS", "AUTH_JWKS_CACHE_SECONDS", c.Auth.JWKSCacheSeconds)

	c.ResourceTicket.Secret = stringEnvPair("AP_CHAT_RESOURCE_TICKET_SECRET", "CHAT_RESOURCE_TICKET_SECRET", c.ResourceTicket.Secret)
	c.ResourceTicket.TTLSeconds = int64EnvPair("AP_CHAT_RESOURCE_TICKET_TTL_SECONDS", "CHAT_RESOURCE_TICKET_TTL_SECONDS", c.ResourceTicket.TTLSeconds)

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
	c.Logging.LLMInteraction.ConsoleCategories = csvEnvPair("AP_DEBUG_LLM_CONSOLE", "DEBUG_LLM_CONSOLE", c.Logging.LLMInteraction.ConsoleCategories)
	c.Logging.LLMInteraction.MaskSensitive = boolEnv("LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE", c.Logging.LLMInteraction.MaskSensitive)
	if strings.TrimSpace(c.Logging.LLMInteraction.RecordDir) == "" {
		c.Logging.LLMInteraction.RecordDir = c.Paths.ChatsDir
	}
	c.Logging.LLMInteraction.RecordEnabled = boolEnvPair("AP_DEBUG_LLM_CHAT_RECORD", "DEBUG_LLM_CHAT_RECORD", c.Logging.LLMInteraction.RecordEnabled)

	c.ContainerHub.BaseURL = stringEnvPair("AP_CONTAINER_HUB_BASE_URL", "CONTAINER_HUB_BASE_URL", c.ContainerHub.BaseURL)
	c.ContainerHub.AuthToken = stringEnvPair("AP_CONTAINER_HUB_AUTH_TOKEN", "CONTAINER_HUB_AUTH_TOKEN", c.ContainerHub.AuthToken)
	c.ContainerHub.DefaultEnvironmentID = stringEnvPair("AP_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID", "CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID", c.ContainerHub.DefaultEnvironmentID)
	c.ContainerHub.RequestTimeout = intEnvPair("AP_CONTAINER_HUB_REQUEST_TIMEOUT", "CONTAINER_HUB_REQUEST_TIMEOUT", c.ContainerHub.RequestTimeout)
	c.ContainerHub.DefaultSandboxLevel = strings.ToLower(stringEnvPair("AP_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL", "CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL", c.ContainerHub.DefaultSandboxLevel))
	c.ContainerHub.AgentIdleTimeout = int64EnvPair("AP_CONTAINER_HUB_AGENT_IDLE_TIMEOUT", "CONTAINER_HUB_AGENT_IDLE_TIMEOUT", c.ContainerHub.AgentIdleTimeout)
	c.ContainerHub.DestroyQueueDelay = int64EnvPair("AP_CONTAINER_HUB_DESTROY_QUEUE_DELAY", "CONTAINER_HUB_DESTROY_QUEUE_DELAY", c.ContainerHub.DestroyQueueDelay)

	c.Run.MaxBackgroundDuration = int64Env("AGENT_RUN_MAX_BACKGROUND_DURATION", c.Run.MaxBackgroundDuration)
	c.Run.MaxDisconnectedWait = int64Env("AGENT_RUN_MAX_DISCONNECTED_WAIT", c.Run.MaxDisconnectedWait)
	c.WebSocket.MaxMessageSizeBytes = intEnv("AGENT_WS_MAX_MESSAGE_SIZE", c.WebSocket.MaxMessageSizeBytes)
	c.WebSocket.PingInterval = int64Env("AGENT_WS_PING_INTERVAL", c.WebSocket.PingInterval)
	c.WebSocket.WriteTimeout = int64Env("AGENT_WS_WRITE_TIMEOUT", c.WebSocket.WriteTimeout)
	c.WebSocket.WriteQueueSize = intEnv("AGENT_WS_WRITE_QUEUE_SIZE", c.WebSocket.WriteQueueSize)
	c.WebSocket.MaxObservesPerConn = intEnv("AGENT_WS_MAX_OBSERVES_PER_CONN", c.WebSocket.MaxObservesPerConn)
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

func stringEnvPair(primaryKey string, legacyKey string, fallback string) string {
	return stringEnv(primaryKey, stringEnv(legacyKey, fallback))
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

func boolEnvPair(primaryKey string, legacyKey string, fallback bool) bool {
	return boolEnv(primaryKey, boolEnv(legacyKey, fallback))
}

func intEnv(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return parseInt(strings.TrimSpace(raw), fallback)
}

func intEnvPair(primaryKey string, legacyKey string, fallback int) int {
	return intEnv(primaryKey, intEnv(legacyKey, fallback))
}

func int64Env(key string, fallback int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return int64(parseInt(strings.TrimSpace(raw), int(fallback)))
}

func int64EnvPair(primaryKey string, legacyKey string, fallback int64) int64 {
	return int64Env(primaryKey, int64Env(legacyKey, fallback))
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

func csvEnvPair(primaryKey string, legacyKey string, fallback []string) []string {
	return csvEnv(primaryKey, csvEnv(legacyKey, fallback))
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
