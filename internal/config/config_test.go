package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Server.Port != "8080" {
			t.Fatalf("expected default port 8080, got %q", cfg.Server.Port)
		}
		if cfg.Paths.RegistriesDir != filepath.Join("runtime", "registries") {
			t.Fatalf("unexpected registries dir: %q", cfg.Paths.RegistriesDir)
		}
		if !cfg.Auth.Enabled {
			t.Fatalf("expected auth enabled by default")
		}
		if cfg.Auth.LocalPublicKeyFile != ProjectFile(filepath.Join("configs", "local-public-key.pem")) {
			t.Fatalf("unexpected default auth public key path: %q", cfg.Auth.LocalPublicKeyFile)
		}
		if !cfg.ChatImage.ResourceTicketEnabled {
			t.Fatalf("expected resource ticket enabled by default")
		}
		if cfg.SSE.HeartbeatIntervalMs != 15000 {
			t.Fatalf("expected default heartbeat interval 15000, got %d", cfg.SSE.HeartbeatIntervalMs)
		}
		if cfg.H2A.Render.HeartbeatPassThrough != true {
			t.Fatalf("expected heartbeat pass-through enabled by default")
		}
		if cfg.Logging.LLMInteraction.MaskSensitive {
			t.Fatalf("expected llm interaction logs to be unmasked by default")
		}
		if cfg.BashHITL.DefaultTimeoutMs != 120000 {
			t.Fatalf("expected default bash HITL timeout 120000, got %d", cfg.BashHITL.DefaultTimeoutMs)
		}
	})
}

func TestLoadAuthLocalPublicKeyPathUnderConfigs(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE": "local-public-key.pem",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		want := ProjectFile(filepath.Join("configs", "local-public-key.pem"))
		if cfg.Auth.LocalPublicKeyFile != want {
			t.Fatalf("expected compat auth public key path %q, got %q", want, cfg.Auth.LocalPublicKeyFile)
		}
	})
}

func TestLoadAuthLocalPublicKeyPathPreservesExplicitConfigsPath(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE": filepath.Join("configs", "custom.pem"),
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		want := ProjectFile(filepath.Join("configs", "custom.pem"))
		if cfg.Auth.LocalPublicKeyFile != want {
			t.Fatalf("expected auth public key path %q, got %q", want, cfg.Auth.LocalPublicKeyFile)
		}
	})
}

func TestLoadAuthLocalPublicKeyPathPreservesAbsolutePath(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE": filepath.Join(string(os.PathSeparator), "tmp", "custom.pem"),
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		want := filepath.Join(string(os.PathSeparator), "tmp", "custom.pem")
		if cfg.Auth.LocalPublicKeyFile != want {
			t.Fatalf("expected absolute auth public key path %q, got %q", want, cfg.Auth.LocalPublicKeyFile)
		}
	})
}

func TestLoadServerPortFromEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"SERVER_PORT": "11949",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Server.Port != "11949" {
			t.Fatalf("expected server port 11949, got %q", cfg.Server.Port)
		}
	})
}

func TestLoadIgnoresHostPortForServerPort(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"HOST_PORT": "11949",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Server.Port != "8080" {
			t.Fatalf("expected default server port 8080 when only HOST_PORT is set, got %q", cfg.Server.Port)
		}
	})
}

func TestLoadCustomStorageDirs(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"CHATS_DIR":  filepath.Join("var", "custom-chats"),
		"MEMORY_DIR": filepath.Join("var", "custom-memory"),
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Paths.ChatsDir != filepath.Join("var", "custom-chats") {
			t.Fatalf("unexpected chats dir: %q", cfg.Paths.ChatsDir)
		}
		if cfg.Paths.MemoryDir != filepath.Join("var", "custom-memory") {
			t.Fatalf("unexpected memory dir: %q", cfg.Paths.MemoryDir)
		}
		if cfg.ChatStorage.Dir != filepath.Join("var", "custom-chats") {
			t.Fatalf("unexpected chat storage dir: %q", cfg.ChatStorage.Dir)
		}
		if cfg.Memory.StorageDir != filepath.Join("var", "custom-memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
	})
}

func TestLoadIgnoresLegacyMemoryStorageEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_MEMORY_STORAGE_DIR": filepath.Join("var", "custom-memory"),
	}, func() {
		if _, err := Load(); err != nil {
			t.Fatalf("expected deprecated env to be ignored, got %v", err)
		}
	})
}

func TestLoadIgnoresDeprecatedEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"RUNTIME_DIR": "runtime",
	}, func() {
		if _, err := Load(); err != nil {
			t.Fatalf("expected deprecated env to be ignored, got %v", err)
		}
	})
}

func TestLoadAcceptsJavaEnvContract(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_AUTH_ENABLED":                      "false",
		"CHAT_IMAGE_TOKEN_SECRET":                 "secret",
		"CHAT_RESOURCE_TICKET_ENABLED":            "true",
		"AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS":   "true",
		"AGENT_SSE_HEARTBEAT_INTERVAL_MS":         "3000",
		"AGENT_H2A_RENDER_FLUSH_INTERVAL_MS":      "25",
		"AGENT_H2A_RENDER_MAX_BUFFERED_CHARS":     "256",
		"AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS":    "3",
		"AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH": "false",
		"AGENT_DEFAULT_REACT_MAX_STEPS":           "12",
		"AGENT_MEMORY_REMEMBER_MODEL_KEY":         "demo-model",
		"AGENT_SCHEDULE_ENABLED":                  "false",
		"AGENT_SCHEDULE_DEFAULT_ZONE_ID":          "Asia/Shanghai",
		"AGENT_SCHEDULE_POOL_SIZE":                "7",
		"LOGGING_AGENT_REQUEST_ENABLED":           "false",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Auth.Enabled {
			t.Fatalf("expected auth disabled from env")
		}
		if cfg.ChatImage.Secret != "secret" {
			t.Fatalf("unexpected chat image secret: %q", cfg.ChatImage.Secret)
		}
		if !cfg.SSE.IncludeToolPayloadEvents {
			t.Fatalf("expected sse tool payload flag enabled")
		}
		if cfg.SSE.HeartbeatIntervalMs != 3000 {
			t.Fatalf("unexpected heartbeat interval: %d", cfg.SSE.HeartbeatIntervalMs)
		}
		if cfg.H2A.Render.FlushIntervalMs != 25 {
			t.Fatalf("unexpected flush interval: %d", cfg.H2A.Render.FlushIntervalMs)
		}
		if cfg.H2A.Render.MaxBufferedChars != 256 {
			t.Fatalf("unexpected max buffered chars: %d", cfg.H2A.Render.MaxBufferedChars)
		}
		if cfg.H2A.Render.MaxBufferedEvents != 3 {
			t.Fatalf("unexpected max buffered events: %d", cfg.H2A.Render.MaxBufferedEvents)
		}
		if cfg.H2A.Render.HeartbeatPassThrough {
			t.Fatalf("expected heartbeat pass-through disabled from env")
		}
		if cfg.Defaults.React.MaxSteps != 12 {
			t.Fatalf("unexpected react max steps: %d", cfg.Defaults.React.MaxSteps)
		}
		if cfg.Memory.RememberModelKey != "demo-model" {
			t.Fatalf("unexpected remember model key: %q", cfg.Memory.RememberModelKey)
		}
		if cfg.Schedule.Enabled {
			t.Fatalf("expected schedule disabled")
		}
		if cfg.Schedule.DefaultZoneID != "Asia/Shanghai" {
			t.Fatalf("unexpected schedule default zone: %q", cfg.Schedule.DefaultZoneID)
		}
		if cfg.Schedule.PoolSize != 7 {
			t.Fatalf("unexpected schedule pool size: %d", cfg.Schedule.PoolSize)
		}
		if cfg.Logging.Request.Enabled {
			t.Fatalf("expected request logging disabled")
		}
	})
}

func TestLoadContainerHubAndBashConfigFromFiles(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.ContainerHub.Enabled {
			t.Fatalf("expected container hub enabled from config file")
		}
		if cfg.ContainerHub.BaseURL == "" {
			t.Fatalf("expected container hub base url")
		}
		if cfg.Bash.ShellExecutable == "" {
			t.Fatalf("expected bash shell executable from config file")
		}
		if len(cfg.Bash.AllowedCommands) == 0 {
			t.Fatalf("expected bash allowed commands from config file")
		}
	})
}

func TestLoadEnvOverridesStructuredConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_CONTAINER_HUB_ENABLED":           "false",
		"AGENT_CONTAINER_HUB_BASE_URL":          "http://127.0.0.1:18000",
		"AGENT_BASH_ALLOWED_COMMANDS":           "pwd,echo",
		"AGENT_BASH_SHELL_FEATURES_ENABLED":     "true",
		"AGENT_BASH_WORKING_DIRECTORY":          filepath.Join("var", "runner"),
		"AGENT_BASH_PATH_CHECK_BYPASS_COMMANDS": "echo",
		"AGENT_BASH_HITL_DEFAULT_TIMEOUT_MS":    "45000",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.ContainerHub.Enabled {
			t.Fatalf("expected container hub env override to disable feature")
		}
		if cfg.ContainerHub.BaseURL != "http://127.0.0.1:18000" {
			t.Fatalf("unexpected base url: %q", cfg.ContainerHub.BaseURL)
		}
		if !cfg.Bash.ShellFeaturesEnabled {
			t.Fatalf("expected shell features enabled from env")
		}
		if cfg.Bash.WorkingDirectory != filepath.Join("var", "runner") {
			t.Fatalf("unexpected working directory: %q", cfg.Bash.WorkingDirectory)
		}
		if len(cfg.Bash.AllowedCommands) != 2 {
			t.Fatalf("unexpected allowed commands: %#v", cfg.Bash.AllowedCommands)
		}
		if len(cfg.Bash.PathCheckBypassCommands) != 1 || cfg.Bash.PathCheckBypassCommands[0] != "echo" {
			t.Fatalf("unexpected path bypass commands: %#v", cfg.Bash.PathCheckBypassCommands)
		}
		if cfg.BashHITL.DefaultTimeoutMs != 45000 {
			t.Fatalf("unexpected bash HITL timeout: %d", cfg.BashHITL.DefaultTimeoutMs)
		}
	})
}

func TestLoadLLMInteractionMaskSensitiveFromEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE": "true",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Logging.LLMInteraction.MaskSensitive {
			t.Fatalf("expected llm interaction masking enabled from env")
		}
	})
}

func withIsolatedEnv(t *testing.T, values map[string]string, fn func()) {
	t.Helper()

	keys := append([]string{}, deprecatedEnvVars...)
	keys = append(keys,
		"HOST_PORT",
		"SERVER_PORT",
		"REGISTRIES_DIR",
		"OWNER_DIR",
		"AGENTS_DIR",
		"TEAMS_DIR",
		"ROOT_DIR",
		"SCHEDULES_DIR",
		"CHATS_DIR",
		"MEMORY_DIR",
		"PAN_DIR",
		"SKILLS_MARKET_DIR",
		"AGENT_CONTAINER_HUB_ENABLED",
		"AGENT_CONTAINER_HUB_BASE_URL",
		"AGENT_CONTAINER_HUB_AUTH_TOKEN",
		"AGENT_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID",
		"AGENT_CONTAINER_HUB_REQUEST_TIMEOUT_MS",
		"AGENT_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL",
		"AGENT_CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS",
		"AGENT_CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS",
		"AGENT_BASH_WORKING_DIRECTORY",
		"AGENT_BASH_ALLOWED_PATHS",
		"AGENT_BASH_ALLOWED_COMMANDS",
		"AGENT_BASH_PATH_CHECKED_COMMANDS",
		"AGENT_BASH_PATH_CHECK_BYPASS_COMMANDS",
		"AGENT_BASH_SHELL_FEATURES_ENABLED",
		"AGENT_BASH_SHELL_EXECUTABLE",
		"AGENT_BASH_SHELL_TIMEOUT_MS",
		"AGENT_BASH_MAX_COMMAND_CHARS",
		"AGENT_BASH_HITL_DEFAULT_TIMEOUT_MS",
		"AGENT_AUTH_ENABLED",
		"AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE",
		"AGENT_AUTH_JWKS_URI",
		"AGENT_AUTH_ISSUER",
		"AGENT_AUTH_JWKS_CACHE_SECONDS",
		"CHAT_IMAGE_TOKEN_SECRET",
		"CHAT_IMAGE_TOKEN_TTL_SECONDS",
		"CHAT_RESOURCE_TICKET_ENABLED",
		"AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS",
		"AGENT_SSE_HEARTBEAT_INTERVAL_MS",
		"AGENT_H2A_RENDER_FLUSH_INTERVAL_MS",
		"AGENT_H2A_RENDER_MAX_BUFFERED_CHARS",
		"AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS",
		"AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH",
		"AGENT_SCHEDULE_ENABLED",
		"AGENT_SCHEDULE_DEFAULT_ZONE_ID",
		"AGENT_SCHEDULE_POOL_SIZE",
		"CHAT_STORAGE_K",
		"CHAT_STORAGE_CHARSET",
		"CHAT_STORAGE_ACTION_TOOLS",
		"CHAT_STORAGE_INDEX_SQLITE_FILE",
		"CHAT_STORAGE_INDEX_AUTO_REBUILD_ON_INCOMPATIBLE_SCHEMA",
		"AGENT_MEMORY_DB_FILE_NAME",
		"AGENT_MEMORY_CONTEXT_TOP_N",
		"AGENT_MEMORY_CONTEXT_MAX_CHARS",
		"AGENT_MEMORY_SEARCH_DEFAULT_LIMIT",
		"AGENT_MEMORY_HYBRID_VECTOR_WEIGHT",
		"AGENT_MEMORY_HYBRID_FTS_WEIGHT",
		"AGENT_MEMORY_DUAL_WRITE_MARKDOWN",
		"AGENT_MEMORY_EMBEDDING_PROVIDER_KEY",
		"AGENT_MEMORY_EMBEDDING_MODEL",
		"AGENT_MEMORY_EMBEDDING_DIMENSION",
		"AGENT_MEMORY_EMBEDDING_TIMEOUT_MS",
		"AGENT_MEMORY_AUTO_REMEMBER_ENABLED",
		"AGENT_MEMORY_REMEMBER_MODEL_KEY",
		"AGENT_MEMORY_REMEMBER_TIMEOUT_MS",
		"AGENT_DEFAULT_MAX_TOKENS",
		"AGENT_DEFAULT_BUDGET_RUN_TIMEOUT_MS",
		"AGENT_DEFAULT_BUDGET_MODEL_MAX_CALLS",
		"AGENT_DEFAULT_BUDGET_MODEL_TIMEOUT_MS",
		"AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT",
		"AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS",
		"AGENT_DEFAULT_BUDGET_TOOL_TIMEOUT_MS",
		"AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT",
		"AGENT_DEFAULT_REACT_MAX_STEPS",
		"AGENT_DEFAULT_PLAN_EXECUTE_MAX_STEPS",
		"AGENT_DEFAULT_PLAN_EXECUTE_MAX_WORK_ROUNDS_PER_TASK",
		"LOGGING_AGENT_REQUEST_ENABLED",
		"LOGGING_AGENT_AUTH_ENABLED",
		"LOGGING_AGENT_EXCEPTION_ENABLED",
		"LOGGING_AGENT_TOOL_ENABLED",
		"LOGGING_AGENT_ACTION_ENABLED",
		"LOGGING_AGENT_VIEWPORT_ENABLED",
		"LOGGING_AGENT_SSE_ENABLED",
		"LOGGING_AGENT_LLM_INTERACTION_ENABLED",
		"LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE",
	)

	previous := map[string]*string{}
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			copied := value
			previous[key] = &copied
		} else {
			previous[key] = nil
		}
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	t.Cleanup(func() {
		for key, value := range previous {
			var err error
			if value == nil {
				err = os.Unsetenv(key)
			} else {
				err = os.Setenv(key, *value)
			}
			if err != nil {
				t.Fatalf("restore %s: %v", key, err)
			}
		}
	})
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	fn()
}
