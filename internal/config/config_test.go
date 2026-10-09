package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/deprecation"
)

func TestDefaultReadonlyRootsMatchToolsExample(t *testing.T) {
	want := []string{"@agent", "@skills"}
	cfg := Config{AccessPolicy: defaultAccessPolicyConfig()}
	if got := cfg.AccessPolicy.Levels["default"].ReadonlyRoots; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected default readonly roots: %v", got)
	}
	tree, err := LoadYAMLTree(filepath.Join("..", "..", "configs", "tools.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.applyAccessPolicyValues(tree.(map[string]any)["access-policy"].(map[string]any))
	if got := cfg.AccessPolicy.Levels["default"].ReadonlyRoots; !reflect.DeepEqual(got, want) {
		t.Fatalf("example readonly roots differ from defaults: %v", got)
	}
}

func TestLoadDefaults(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		runtimeConfig := ""
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeConfig, func() {
			withProjectFileContents(t, filepath.Join("configs", "kbase-settings.yml"), nil, func() {
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
				if cfg.Paths.ToolsDir != filepath.Join("runtime", "tools") {
					t.Fatalf("unexpected tools dir: %q", cfg.Paths.ToolsDir)
				}
				if cfg.Paths.RUAgentsDir != ProjectFile(filepath.Join("runtime", "ru-agents")) {
					t.Fatalf("unexpected ru-agents dir: %q", cfg.Paths.RUAgentsDir)
				}
				if cfg.IdentityFile != ProjectFile(filepath.Join("runtime", ".state", "identity", "access-token")) {
					t.Fatalf("unexpected default identity file: %q", cfg.IdentityFile)
				}

				if !cfg.Auth.Enabled {
					t.Fatalf("expected auth enabled by default")
				}
				if cfg.Auth.LocalPublicKeyFile != ProjectFile(filepath.Join("configs", "local-public-key.pem")) {
					t.Fatalf("unexpected default auth public key path: %q", cfg.Auth.LocalPublicKeyFile)
				}
				if cfg.ResourceTicket.Enabled() {
					t.Fatalf("expected resource ticket disabled by default")
				}
				if cfg.ResourceTicket.TTLSeconds != 86400 {
					t.Fatalf("expected default resource ticket ttl 86400, got %d", cfg.ResourceTicket.TTLSeconds)
				}
				if got := strings.Join(cfg.CORS.ExposedHeaders, ","); got != "Content-Type,X-Document-Kind,X-Document-Revision" {
					t.Fatalf("unexpected default CORS exposed headers: %q", got)
				}
				if cfg.Billing.Currency != "CNY" {
					t.Fatalf("expected default billing currency CNY, got %q", cfg.Billing.Currency)
				}
				if cfg.Query.AdvancedUserPrompt {
					t.Fatalf("expected advanced user prompt disabled by default")
				}
				if cfg.SSE.HeartbeatInterval != 30 {
					t.Fatalf("expected default heartbeat interval 30, got %d", cfg.SSE.HeartbeatInterval)
				}
				if cfg.WebSocket.PingInterval != 30 || cfg.WebSocket.PongTimeout != 60 || cfg.WebSocket.HeartbeatInterval != 30 || cfg.WebSocket.ClientSilenceTimeout != 100 {
					t.Fatalf("unexpected websocket liveness defaults: %#v", cfg.WebSocket)
				}
				if !cfg.Logging.Request.Enabled ||
					!cfg.Logging.Auth.Enabled ||
					!cfg.Logging.Exception.Enabled ||
					!cfg.Logging.Tool.Enabled ||
					!cfg.Logging.Action.Enabled ||
					!cfg.Logging.View.Enabled ||
					!cfg.Logging.LLMInteraction.Enabled {
					t.Fatalf("expected default logging surfaces enabled, got %#v", cfg.Logging)
				}
				if cfg.Logging.SSE.Enabled {
					t.Fatalf("expected sse logging disabled by default")
				}
				if cfg.Logging.LLMInteraction.MaskSensitive {
					t.Fatalf("expected llm interaction logs to be unmasked by default")
				}
				if got, want := strings.Join(cfg.Logging.LLMInteraction.ConsoleCategories, ","), "request,usage"; got != want {
					t.Fatalf("expected default llm console categories %q, got %q", want, got)
				}
				if cfg.Logging.LLMInteraction.RecordEnabled {
					t.Fatalf("expected llm chat record disabled by default")
				}
				if cfg.Logging.LLMInteraction.RecordDir != filepath.Join("runtime", "chats") {
					t.Fatalf("unexpected llm chat record dir: %q", cfg.Logging.LLMInteraction.RecordDir)
				}
				if cfg.ContainerHub.AuthToken != "" || cfg.ContainerHub.DefaultEnvironmentID != "" {
					t.Fatalf("expected empty container hub token/environment defaults, got %#v", cfg.ContainerHub)
				}
				if cfg.ContainerHub.RequestTimeout != 300 ||
					cfg.ContainerHub.DefaultSandboxLevel != "run" ||
					cfg.ContainerHub.AgentIdleTimeout != 600 ||
					cfg.ContainerHub.DestroyQueueDelay != 5 {
					t.Fatalf("unexpected container hub runtime defaults: %#v", cfg.ContainerHub)
				}
				if cfg.Defaults.Budget.Hitl.Timeout != 0 {
					t.Fatalf("expected default HITL budget timeout 0, got %d", cfg.Defaults.Budget.Hitl.Timeout)
				}
				if cfg.Defaults.Budget.Hitl.Question.Timeout != 0 || cfg.Defaults.Budget.Hitl.Approval.Timeout != 0 ||
					cfg.Defaults.Budget.Hitl.Form.Timeout != 0 {
					t.Fatalf("expected default HITL mode timeouts unset, got %#v", cfg.Defaults.Budget.Hitl)
				}
				if cfg.Defaults.Budget.Timeout != 3600 {
					t.Fatalf("expected default budget timeout 3600, got %d", cfg.Defaults.Budget.Timeout)
				}
				if cfg.Defaults.Budget.Model.Timeout != 180 {
					t.Fatalf("expected default model timeout 180, got %d", cfg.Defaults.Budget.Model.Timeout)
				}
				if cfg.Defaults.Budget.Model.MaxCalls != 100 {
					t.Fatalf("expected default model max calls 100, got %d", cfg.Defaults.Budget.Model.MaxCalls)
				}
				if cfg.Defaults.Budget.Model.RetryCount != 5 {
					t.Fatalf("expected default model retry count 5, got %d", cfg.Defaults.Budget.Model.RetryCount)
				}
				if cfg.Defaults.Budget.MaxSteps != 100 {
					t.Fatalf("expected default budget max steps 100, got %d", cfg.Defaults.Budget.MaxSteps)
				}
				if cfg.Defaults.Budget.Tool.MaxCalls != 100 {
					t.Fatalf("expected default tool max calls 100, got %d", cfg.Defaults.Budget.Tool.MaxCalls)
				}
				if cfg.Defaults.Budget.Tool.Timeout != 600 {
					t.Fatalf("expected default tool timeout 600, got %d", cfg.Defaults.Budget.Tool.Timeout)
				}
				if !cfg.Memory.Enabled {
					t.Fatalf("expected Markdown memory runtime enabled by default")
				}
				if cfg.RuntimeMode != RuntimeModeStandalone {
					t.Fatalf("unexpected default runtime mode: %q", cfg.RuntimeMode)
				}
				defaultLevel := cfg.AccessPolicy.Levels["default"]
				if got := strings.Join(defaultLevel.ReadRoots, ","); got != "@workspace,@chat,@agent,@skills,@temp" {
					t.Fatalf("unexpected default access-policy read roots: %#v", defaultLevel.ReadRoots)
				}
				if got := strings.Join(defaultLevel.WriteRoots, ","); got != "@chat,@temp" {
					t.Fatalf("unexpected default access-policy write roots: %#v", defaultLevel.WriteRoots)
				}
			})
		})
	})
}

func TestLoadExplicitIdentityFileOverridesRuntimeDefault(t *testing.T) {
	withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": filepath.Join(t.TempDir(), "runtime"), "AP_RUNTIME_STATE_DIR": filepath.Join(t.TempDir(), "state")}, func() {
		identityFile := filepath.Join(t.TempDir(), "desktop state", "sso-access-token.txt")
		cfg, err := Load(LoadOptions{IdentityFile: identityFile})
		if err != nil {
			t.Fatalf("load config with missing identity file: %v", err)
		}
		if cfg.IdentityFile != filepath.Clean(identityFile) {
			t.Fatalf("identity file = %q, want %q", cfg.IdentityFile, filepath.Clean(identityFile))
		}
	})
}

func TestLoadRuntimeModeFromStartupOptions(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		cfg, err := Load(LoadOptions{RuntimeMode: "desktop"})
		if err != nil {
			t.Fatalf("load desktop runtime mode: %v", err)
		}
		if cfg.RuntimeMode != RuntimeModeDesktop {
			t.Fatalf("runtime mode = %q, want desktop", cfg.RuntimeMode)
		}
		if _, err := Load(LoadOptions{RuntimeMode: "browser"}); err == nil || !strings.Contains(err.Error(), "standalone or desktop") {
			t.Fatalf("expected invalid runtime mode error, got %v", err)
		}
	})
}

func TestLoadDerivesIdentityFileFromRuntimeDir(t *testing.T) {
	t.Run("absolute", func(t *testing.T) {
		runtimeRoot := filepath.Join(t.TempDir(), "runtime")
		withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": runtimeRoot}, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			want := filepath.Join(runtimeRoot, ".state", "identity", "access-token")
			if cfg.IdentityFile != want {
				t.Fatalf("identity file = %q, want %q", cfg.IdentityFile, want)
			}
		})
	})

	t.Run("relative to config root", func(t *testing.T) {
		configRoot := t.TempDir()
		runtimeRoot := filepath.Join("var", "runtime")
		withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": runtimeRoot}, func() {
			cfg, err := Load(LoadOptions{ConfigDir: configRoot})
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			want := filepath.Join(configRoot, runtimeRoot, ".state", "identity", "access-token")
			if cfg.IdentityFile != want {
				t.Fatalf("identity file = %q, want %q", cfg.IdentityFile, want)
			}
		})
	})

	t.Run("home relative", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolve user home: %v", err)
		}
		runtimeRoot := filepath.Join("~", "agent-platform-runtime")
		withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": runtimeRoot}, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			want := filepath.Join(home, "agent-platform-runtime", ".state", "identity", "access-token")
			if cfg.IdentityFile != want {
				t.Fatalf("identity file = %q, want %q", cfg.IdentityFile, want)
			}
		})
	})
}

func TestLoadRejectsRelativeIdentityFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		if _, err := Load(LoadOptions{IdentityFile: "state/desktop/sso-access-token.txt"}); err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Fatalf("expected relative identity file error, got %v", err)
		}
	})
}

func TestContainerHubPublicTemplatesExposeRuntimeDefaults(t *testing.T) {
	runtimeExampleBytes, err := os.ReadFile(ProjectFile("configs/runtime.example.yml"))
	if err != nil {
		t.Fatalf("read runtime example: %v", err)
	}
	runtimeExample := string(runtimeExampleBytes)
	for _, want := range []string{
		"resource:\n",
		"  ticket-ttl-seconds: 86400\n",
		"kbx:\n",
		"container-hub:\n",
		"  base-url: ${AP_CONTAINER_HUB_BASE_URL:http://host.docker.internal:11960}\n",
		"  # auth-token:\n",
		"  default-environment-id:\n",
		"  request-timeout: 300\n",
		"  default-sandbox-level: run\n",
		"  agent-idle-timeout: 600\n",
		"  destroy-queue-delay: 5\n",
	} {
		if !strings.Contains(runtimeExample, want) {
			t.Fatalf("expected runtime example to contain %q", want)
		}
	}
	if strings.Contains(runtimeExample, "  auth-token:\n") {
		t.Fatalf("expected runtime example auth-token to remain commented")
	}
	if strings.Contains(runtimeExample, "server:\n") || strings.Contains(runtimeExample, "port: 11949\n") {
		t.Fatalf("expected runtime example not to expose server port config")
	}
	if strings.Contains(runtimeExample, "\nkbase:\n") {
		t.Fatalf("expected runtime example to move kbase behavior settings to kbase-settings.example.yml")
	}
	for _, forbidden := range []string{
		"anthropic:\n",
		"  max-output-tokens: 4096\n",
		"logging:\n",
		"llm-interaction:\n",
	} {
		if strings.Contains(runtimeExample, forbidden) {
			t.Fatalf("expected runtime example not to expose %q", forbidden)
		}
	}

	var merged Config
	if err := merged.applyAgentSettingsFile(ProjectFile("configs/agent-settings.example.yml")); err != nil {
		t.Fatal(err)
	}
	if err := merged.applyAgentPromptFile(ProjectFile("configs/agent-prompt.example.yml")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged.CoderPrompts.SystemPrompt, "```echarts") || !strings.Contains(merged.KBasePrompts.CapabilityPrompt, "kbase_search") ||
		!strings.Contains(merged.KBasePrompts.WorkspacePrompt, "{{workspace_dir}}") || strings.TrimSpace(merged.KBasePrompts.EditingPrompt) == "" ||
		strings.Contains(merged.KBasePrompts.SystemPrompt, "kbase_search") {
		t.Fatal("merged prompts lost content")
	}
	envExampleBytes, err := os.ReadFile(ProjectFile(".env.example"))
	if err != nil {
		t.Fatalf("read env example: %v", err)
	}
	envExample := string(envExampleBytes)
	allowedEnvExampleKeys := map[string]bool{
		"SERVER_PORT":                    true,
		"AP_RUNTIME_DIR":                 true,
		"AP_RUNTIME_REGISTRIES_DIR":      true,
		"AP_RUNTIME_CHATS_DIR":           true,
		"AP_RUNTIME_MEMORY_DIR":          true,
		"AP_RUNTIME_PAN_DIR":             true,
		"AP_RUNTIME_STATE_DIR":           true,
		"AP_CONTAINER_HUB_BASE_URL":      true,
		"AP_CHAT_RESOURCE_TICKET_SECRET": true,
		"AP_DEBUG_LLM_CONSOLE":           true,
		"AP_DEBUG_LLM_CHAT_RECORD":       true,
	}
	seenEnvExampleKeys := map[string]bool{}
	for _, key := range envExampleKeys(envExample) {
		if !allowedEnvExampleKeys[key] {
			t.Fatalf("expected env example key %q to be absent from allowlist-only .env.example", key)
		}
		seenEnvExampleKeys[key] = true
	}
	for key := range allowedEnvExampleKeys {
		if !seenEnvExampleKeys[key] {
			t.Fatalf("expected env example to contain allowlist key %q", key)
		}
	}
	for _, want := range []string{
		"# Resource access tickets\n",
		"# AP_CHAT_RESOURCE_TICKET_SECRET=replace-with-your-resource-ticket-secret\n",
		"AP_CONTAINER_HUB_BASE_URL=http://127.0.0.1:11960\n",
	} {
		if !strings.Contains(envExample, want) {
			t.Fatalf("expected env example to contain %q", want)
		}
	}
	for _, forbidden := range []string{
		"AP_CONTAINER_HUB_AUTH_TOKEN",
		"AP_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID",
		"AP_CONTAINER_HUB_REQUEST_TIMEOUT",
		"AP_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL",
		"AP_CONTAINER_HUB_AGENT_IDLE_TIMEOUT",
		"AP_CONTAINER_HUB_DESTROY_QUEUE_DELAY",
		"AP_STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS",
		"STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS",
	} {
		if strings.Contains(envExample, forbidden) {
			t.Fatalf("expected env example not to contain %q", forbidden)
		}
	}
}

func envExampleKeys(content string) []string {
	var keys []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if trimmed == "" || !strings.Contains(trimmed, "=") {
			continue
		}
		key, _, _ := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func TestLoadRuntimeBudgetYAMLIgnoresRemovedAnthropicDefaults(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_DEFAULT_MAX_OUTPUT_TOKENS": "8192",
		"AGENT_DEFAULT_BUDGET_MAX_STEPS":  "17",
	}, func() {
		content := "" +
			"anthropic:\n" +
			"  max-output-tokens: 8192\n" +
			"defaults:\n" +
			"  max-output-tokens: 9999\n" +
			"budget:\n" +
			"  max-steps: 17\n" +
			"  tool:\n" +
			"    max-calls: 34\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Defaults.Budget.MaxSteps != 17 {
				t.Fatalf("expected runtime yaml max steps 17, got %d", cfg.Defaults.Budget.MaxSteps)
			}
			if cfg.Defaults.Budget.Tool.MaxCalls != 34 {
				t.Fatalf("expected runtime yaml tool max calls 34, got %d", cfg.Defaults.Budget.Tool.MaxCalls)
			}
		})
	})
}

func TestLoadRuntimeCoderPlanningDefaults(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "defaults:\n  coder-planning:\n    max-steps: 73\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Defaults.CoderPlanning.MaxSteps != 73 {
				t.Fatalf("coder planning max steps = %d, want 73", cfg.Defaults.CoderPlanning.MaxSteps)
			}
		})
	})
}

func TestLoadRuntimeConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"budget:\n" +
			"  timeout: 301\n" +
			"  model:\n" +
			"    timeout: 121\n" +
			"  tool:\n" +
			"    timeout: 122\n" +
			"  stages:\n" +
			"    execute:\n" +
			"      maxSteps: 9\n" +
			"      tool:\n" +
			"        timeout: 123\n" +
			"  hitl:\n" +
			"    timeout: 610\n" +
			"    question:\n" +
			"      timeout: 620\n" +
			"    approval:\n" +
			"      timeout: 630\n" +
			"    form:\n" +
			"      timeout: 640\n" +
			"    plan:\n" +
			"      timeout: 650\n" +
			"resource:\n" +
			"  ticket-ttl-seconds: 777\n" +
			"query:\n" +
			"  advanced-user-prompt: true\n" +
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n" +
			"  auth-token: runtime-token\n" +
			"  default-environment-id: runtime-env\n" +
			"  request-timeout: 123\n" +
			"  default-sandbox-level: agent\n" +
			"  agent-idle-timeout: 654321\n" +
			"  destroy-queue-delay: 2345\n" +
			"cors:\n" +
			"  enabled: true\n" +
			"  path-pattern: /runtime/**\n" +
			"  allowed-origin-patterns:\n" +
			"    - http://runtime.local\n" +
			"  allowed-methods: [GET, POST]\n" +
			"  allowed-headers: [X-Runtime]\n" +
			"  exposed-headers: [X-Expose]\n" +
			"  allow-credentials: true\n" +
			"  max-age-seconds: 99\n" +
			"billing:\n" +
			"  currency: USD\n"
		withProjectFileContents(t, filepath.Join("configs", "container-hub.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "cors.yml"), nil, func() {
				withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
					cfg, err := Load()
					if err != nil {
						t.Fatalf("load config: %v", err)
					}
					if cfg.ContainerHub.BaseURL != "http://runtime-hub" || cfg.ContainerHub.AuthToken != "runtime-token" || cfg.ContainerHub.DefaultEnvironmentID != "runtime-env" {
						t.Fatalf("unexpected container hub identity: %#v", cfg.ContainerHub)
					}
					if cfg.ContainerHub.RequestTimeout != 123 || cfg.ContainerHub.DefaultSandboxLevel != "agent" || cfg.ContainerHub.AgentIdleTimeout != 654321 || cfg.ContainerHub.DestroyQueueDelay != 2345 {
						t.Fatalf("unexpected container hub runtime settings: %#v", cfg.ContainerHub)
					}
					if cfg.ResourceTicket.TTLSeconds != 777 {
						t.Fatalf("unexpected resource ticket ttl: %d", cfg.ResourceTicket.TTLSeconds)
					}
					if !cfg.Query.AdvancedUserPrompt {
						t.Fatalf("expected advanced user prompt from runtime yaml")
					}
					if !cfg.CORS.Enabled || cfg.CORS.PathPattern != "/runtime/**" || !cfg.CORS.AllowCredentials || cfg.CORS.MaxAgeSeconds != 99 {
						t.Fatalf("unexpected cors scalar config: %#v", cfg.CORS)
					}
					if strings.Join(cfg.CORS.AllowedOriginPatterns, ",") != "http://runtime.local" || strings.Join(cfg.CORS.AllowedMethods, ",") != "GET,POST" || strings.Join(cfg.CORS.AllowedHeaders, ",") != "X-Runtime" || strings.Join(cfg.CORS.ExposedHeaders, ",") != "X-Expose" {
						t.Fatalf("unexpected cors list config: %#v", cfg.CORS)
					}
					if cfg.Billing.Currency != "USD" {
						t.Fatalf("unexpected billing currency: %#v", cfg.Billing)
					}
					if cfg.Defaults.Budget.Timeout != 301 ||
						cfg.Defaults.Budget.Model.Timeout != 121 ||
						cfg.Defaults.Budget.Tool.Timeout != 122 ||
						cfg.Defaults.Budget.Stages["execute"].MaxSteps != 9 ||
						cfg.Defaults.Budget.Stages["execute"].Tool.Timeout != 123 {
						t.Fatalf("unexpected runtime budget config: %#v", cfg.Defaults.Budget)
					}
					if cfg.Defaults.Budget.Hitl.Timeout != 610 ||
						cfg.Defaults.Budget.Hitl.Question.Timeout != 620 ||
						cfg.Defaults.Budget.Hitl.Approval.Timeout != 630 ||
						cfg.Defaults.Budget.Hitl.Form.Timeout != 640 {
						t.Fatalf("unexpected runtime HITL budget config: %#v", cfg.Defaults.Budget.Hitl)
					}
				})
			})
		})
	})
}

func TestLoadAPContainerHubBaseURLEnvOverridesRuntimeYAMLConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_CONTAINER_HUB_BASE_URL": "http://env-hub",
	}, func() {
		content := "" +
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n" +
			"  request-timeout: 111\n"
		withProjectFileContents(t, filepath.Join("configs", "container-hub.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.ContainerHub.BaseURL != "http://env-hub" {
					t.Fatalf("expected AP env container hub base url to win, got %q", cfg.ContainerHub.BaseURL)
				}
				if cfg.ContainerHub.RequestTimeout != 111 {
					t.Fatalf("expected runtime yaml timeout to remain, got %d", cfg.ContainerHub.RequestTimeout)
				}
			})
		})
	})
}

func TestLoadVisionRecognizeMissingFileDefaultsDisabled(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "vision-recognize.yml"), nil, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.VisionRecognize.Enabled {
					t.Fatal("expected vision_recognize disabled by default")
				}
				if cfg.VisionRecognize.DefaultProfile != "general" {
					t.Fatalf("unexpected default profile: %q", cfg.VisionRecognize.DefaultProfile)
				}
			})
		})
	})
}

func TestLoadVisionRecognizeConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"vision-recognize:\n" +
			"  enabled: true\n" +
			"  default-profile: ocr\n" +
			"  profiles:\n" +
			"    ocr:\n" +
			"      model-key: bailian-qwen3_5-plus\n" +
			"      timeout: 12\n" +
			"      max-images: 3\n" +
			"      max-image-bytes: 456789\n" +
			"      output-format: json\n" +
			"      system-prompt: |\n" +
			"        extract text\n" +
			"        return json\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !cfg.VisionRecognize.Enabled {
				t.Fatal("expected vision_recognize enabled")
			}
			if cfg.VisionRecognize.DefaultProfile != "ocr" {
				t.Fatalf("unexpected default profile: %q", cfg.VisionRecognize.DefaultProfile)
			}
			profile := cfg.VisionRecognize.Profiles["ocr"]
			if profile.ModelKey != "bailian-qwen3_5-plus" || profile.Timeout != 12 || profile.MaxImages != 3 || profile.MaxImageBytes != 456789 || profile.OutputFormat != "json" {
				t.Fatalf("unexpected profile: %#v", profile)
			}
			if profile.SystemPrompt != "extract text\nreturn json" {
				t.Fatalf("unexpected system prompt: %q", profile.SystemPrompt)
			}
		})
	})
}

func TestLoadWebFetchMissingFileDefaultsDisabled(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.WebFetch.Enabled {
				t.Fatal("expected web_fetch disabled by default")
			}
			if cfg.WebFetch.DefaultProfile != "general" {
				t.Fatalf("unexpected default profile: %q", cfg.WebFetch.DefaultProfile)
			}
		})
	})
}

func TestLoadWebFetchConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"web-fetch:\n" +
			"  enabled: true\n" +
			"  default-profile: summary\n" +
			"  preapproved-hosts:\n" +
			"    - Example.com.\n" +
			"    - '*.Docs.Example.com'\n" +
			"  profiles:\n" +
			"    summary:\n" +
			"      model-key: th-minimax-m3\n" +
			"      timeout: 11\n" +
			"      fetch-timeout: 12\n" +
			"      max-url-length: 456\n" +
			"      max-response-bytes: 789\n" +
			"      max-markdown-chars: 1234\n" +
			"      max-output-tokens: 321\n" +
			"      system-prompt: |\n" +
			"        summarize web pages\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !cfg.WebFetch.Enabled {
				t.Fatal("expected web_fetch enabled")
			}
			if cfg.WebFetch.DefaultProfile != "summary" {
				t.Fatalf("unexpected default profile: %q", cfg.WebFetch.DefaultProfile)
			}
			if got := strings.Join(cfg.WebFetch.PreapprovedHosts, ","); got != "example.com,*.docs.example.com" {
				t.Fatalf("unexpected preapproved hosts: %q", got)
			}
			profile := cfg.WebFetch.Profiles["summary"]
			if profile.ModelKey != "th-minimax-m3" || profile.Timeout != 11 || profile.FetchTimeout != 12 || profile.MaxURLLength != 456 || profile.MaxResponseBytes != 789 || profile.MaxMarkdownChars != 1234 || profile.MaxOutputTokens != 321 {
				t.Fatalf("unexpected profile: %#v", profile)
			}
			if profile.SystemPrompt != "summarize web pages" {
				t.Fatalf("unexpected system prompt: %q", profile.SystemPrompt)
			}
		})
	})
}

func TestLoadImageGenerateMissingFileDefaultsDisabled(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.ImageGenerate.Enabled {
				t.Fatal("expected image_generate disabled by default")
			}
			if cfg.ImageGenerate.DefaultProfile != "general" {
				t.Fatalf("unexpected default profile: %q", cfg.ImageGenerate.DefaultProfile)
			}
		})
	})
}

func TestLoadImageGenerateConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"image-generate:\n" +
			"  enabled: true\n" +
			"  default-profile: general\n" +
			"  profiles:\n" +
			"    general:\n" +
			"      model-key: babelark-gemini-3_1-flash-image\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !cfg.ImageGenerate.Enabled {
				t.Fatal("expected image_generate enabled")
			}
			if cfg.ImageGenerate.DefaultProfile != "general" {
				t.Fatalf("unexpected default profile: %q", cfg.ImageGenerate.DefaultProfile)
			}
			profile := cfg.ImageGenerate.Profiles["general"]
			if profile.ModelKey != "babelark-gemini-3_1-flash-image" ||
				profile.Timeout != 0 ||
				profile.Size != "1024x1024" ||
				profile.ResponseFormat != "b64_json" ||
				profile.OutputMimeType != "image/png" ||
				profile.MaxPromptChars != 4000 ||
				profile.MaxImages != 4 ||
				profile.MaxImageBytes != 20<<20 ||
				!profile.PersistArtifact {
				t.Fatalf("unexpected profile defaults: %#v", profile)
			}
		})
	})
}

func TestLoadImageGenerateConfigRejectsProfileEndpointOverride(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"image-generate:\n" +
			"  enabled: true\n" +
			"  profiles:\n" +
			"    general:\n" +
			"      model-key: image-model\n" +
			"      endpoint-path: /v1/images/generations\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "tools.image-generate.profiles.general.endpoint-path") {
				t.Fatalf("expected endpoint override rejection, got %v", err)
			}
		})
	})
}

func TestLoadAIToolsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"vision-recognize:\n" +
			"  enabled: true\n" +
			"  default-profile: ocr\n" +
			"  profiles:\n" +
			"    ocr:\n" +
			"      model-key: bailian-qwen3_5-plus\n" +
			"      timeout: 23\n" +
			"      max-images: 2\n" +
			"      max-image-bytes: 567890\n" +
			"      output-format: json\n" +
			"      system-prompt: |\n" +
			"        extract merged text\n" +
			"image-generate:\n" +
			"  enabled: false\n" +
			"  profiles: {}\n"

		withProjectFileContents(t, filepath.Join("configs", "vision-recognize.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if !cfg.VisionRecognize.Enabled {
					t.Fatal("expected vision_recognize enabled")
				}
				if cfg.VisionRecognize.DefaultProfile != "ocr" {
					t.Fatalf("unexpected default profile: %q", cfg.VisionRecognize.DefaultProfile)
				}
				profile := cfg.VisionRecognize.Profiles["ocr"]
				if profile.ModelKey != "bailian-qwen3_5-plus" || profile.Timeout != 23 || profile.MaxImages != 2 || profile.MaxImageBytes != 567890 || profile.OutputFormat != "json" {
					t.Fatalf("unexpected profile: %#v", profile)
				}
				if profile.SystemPrompt != "extract merged text" {
					t.Fatalf("unexpected system prompt: %q", profile.SystemPrompt)
				}
			})
		})
	})
}

func TestLoadAuthLocalPublicKeyPathIsFixed(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"auth:\n" +
			"  local-public-key-file: configs/runtime-auth.pem\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			want := ProjectFile(filepath.Join("configs", "local-public-key.pem"))
			if cfg.Auth.LocalPublicKeyFile != want {
				t.Fatalf("expected fixed auth public key path %q, got %q", want, cfg.Auth.LocalPublicKeyFile)
			}
		})
	})
}

func TestLoadAuthConfigFromRuntimeYAML(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"auth:\n" +
			"  enabled: false\n" +
			"  jwks-uri: https://issuer.example/.well-known/jwks.json\n" +
			"  issuer: runtime-issuer\n" +
			"  jwks-cache-seconds: 45\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Auth.Enabled {
				t.Fatalf("expected auth disabled from runtime yaml")
			}
			if cfg.Auth.LocalPublicKeyFile != "" {
				t.Fatalf("expected local public key path to be empty in jwks mode, got %q", cfg.Auth.LocalPublicKeyFile)
			}
			if cfg.Auth.JWKSURI != "https://issuer.example/.well-known/jwks.json" ||
				cfg.Auth.Issuer != "runtime-issuer" ||
				cfg.Auth.JWKSCacheSeconds != 45 {
				t.Fatalf("unexpected auth runtime config: %#v", cfg.Auth)
			}
		})
	})
}

func TestLoadUsesConfigDirOptionForStructuredFilesAndAuthKey(t *testing.T) {
	configDir := t.TempDir()
	configsDir := filepath.Join(configDir, "configs")
	if err := os.MkdirAll(configsDir, 0o755); err != nil {
		t.Fatalf("create configs dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "agent-prompt.yml"),
		[]byte("shared:\n  skill:\n    catalog-header: service config header\ncoder:\n  system-prompt: service coder system\n  planning-prompt: service coder plan\n"),
		0o644,
	); err != nil {
		t.Fatalf("write prompts config: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "tools.yml"),
		[]byte("vision-recognize:\n  enabled: true\n  default-profile: service\n"),
		0o644,
	); err != nil {
		t.Fatalf("write ai tools config: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "tools.yml"),
		[]byte("vision-recognize:\n  enabled: true\n  default-profile: service\nbash:\n  shell-executable: service-shell\n"),
		0o644,
	); err != nil {
		t.Fatalf("write tools config: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "runtime.yml"),
		[]byte("container-hub:\n  base-url: http://service-hub\ncors:\n  enabled: true\n"),
		0o644,
	); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	withIsolatedEnv(t, nil, func() {
		cfg, err := Load(LoadOptions{ConfigDir: configDir})
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Prompts.Skill.CatalogHeader != "service config header" {
			t.Fatalf("expected prompts from service config dir, got %q", cfg.Prompts.Skill.CatalogHeader)
		}
		if cfg.CoderPrompts.PlanningPrompt != "service coder plan" {
			t.Fatalf("expected coder prompts from service config dir, got %q", cfg.CoderPrompts.PlanningPrompt)
		}
		if cfg.CoderPrompts.SystemPrompt != "service coder system" {
			t.Fatalf("expected coder system prompt from service config dir, got %q", cfg.CoderPrompts.SystemPrompt)
		}
		if !cfg.VisionRecognize.Enabled || cfg.VisionRecognize.DefaultProfile != "service" {
			t.Fatalf("expected ai tools from service config dir, got %#v", cfg.VisionRecognize)
		}
		if cfg.Bash.ShellExecutable != "service-shell" {
			t.Fatalf("expected tools from service config dir, got %q", cfg.Bash.ShellExecutable)
		}
		if cfg.ContainerHub.BaseURL != "http://service-hub" || !cfg.CORS.Enabled {
			t.Fatalf("expected runtime config from service config dir, got hub=%#v cors=%#v", cfg.ContainerHub, cfg.CORS)
		}
		wantKeyPath := filepath.Join(configDir, "configs", "local-public-key.pem")
		if cfg.Auth.LocalPublicKeyFile != wantKeyPath {
			t.Fatalf("expected auth public key path %q, got %q", wantKeyPath, cfg.Auth.LocalPublicKeyFile)
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

func TestLoadServerPortIgnoresRuntimeFile(t *testing.T) {
	runtimeConfig := "server:\n  port: 7078\n"
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeConfig, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Server.Port != "8080" {
				t.Fatalf("expected runtime server port to be ignored, got %q", cfg.Server.Port)
			}
		})
	})
}

func TestLoadPortOptionIgnoresServerPortEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"SERVER_PORT": "11949",
	}, func() {
		cfg, err := Load(LoadOptions{Port: "7078"})
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Server.Port != "7078" {
			t.Fatalf("expected server port 7078, got %q", cfg.Server.Port)
		}
	})
}

func TestLoadCustomStorageDirs(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_RUNTIME_CHATS_DIR":  filepath.Join("var", "custom-chats"),
		"AP_RUNTIME_MEMORY_DIR": filepath.Join("var", "custom-memory"),
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
		if cfg.Logging.LLMInteraction.RecordDir != filepath.Join("var", "custom-chats") {
			t.Fatalf("unexpected llm chat record dir: %q", cfg.Logging.LLMInteraction.RecordDir)
		}
	})
}

func TestLoadRuntimeDirDerivesRuntimePaths(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_RUNTIME_DIR": filepath.Join("var", "runtime"),
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		runtimeRoot := filepath.Join("var", "runtime")
		if cfg.Paths.RegistriesDir != filepath.Join(runtimeRoot, "registries") {
			t.Fatalf("unexpected registries dir: %q", cfg.Paths.RegistriesDir)
		}
		if cfg.Paths.ChatsDir != filepath.Join(runtimeRoot, "chats") {
			t.Fatalf("unexpected chats dir: %q", cfg.Paths.ChatsDir)
		}
		if cfg.Paths.MemoryDir != filepath.Join(runtimeRoot, "memory") {
			t.Fatalf("unexpected memory dir: %q", cfg.Paths.MemoryDir)
		}
		if cfg.Paths.PanDir != filepath.Join(runtimeRoot, "pan") {
			t.Fatalf("unexpected pan dir: %q", cfg.Paths.PanDir)
		}
		if cfg.Paths.SkillsCenterDir != filepath.Join(runtimeRoot, "skills-center") {
			t.Fatalf("unexpected skills center dir: %q", cfg.Paths.SkillsCenterDir)
		}
		if cfg.Providers.ExternalDir != filepath.Join(runtimeRoot, "registries", "providers") {
			t.Fatalf("unexpected providers dir: %q", cfg.Providers.ExternalDir)
		}
		if cfg.Models.ExternalDir != filepath.Join(runtimeRoot, "registries", "models") {
			t.Fatalf("unexpected models dir: %q", cfg.Models.ExternalDir)
		}
	})
}

func TestLoadRejectsRemovedSkillsMarketPathKey(t *testing.T) {
	runtimeConfig := "paths:\n  skills-market-dir: var/removed-skills-market\n"
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeConfig, func() {
			withProjectFileContents(t, filepath.Join("configs", "kbase-settings.yml"), nil, func() {
				_, err := Load()
				if err == nil || !deprecation.Is(err) || !strings.Contains(err.Error(), "paths configuration was removed") {
					t.Fatalf("expected removed skills-market-dir error, got %v", err)
				}
			})
		})
	})
}

func TestLoadRejectsRemovedSkillsMarketRuntimeDirectory(t *testing.T) {
	for _, withCenter := range []bool{false, true} {
		t.Run(fmt.Sprintf("center_exists_%t", withCenter), func(t *testing.T) {
			runtimeRoot := t.TempDir()
			legacyDir := filepath.Join(runtimeRoot, "skills-market")
			if err := os.MkdirAll(legacyDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if withCenter {
				if err := os.MkdirAll(filepath.Join(runtimeRoot, "skills-center"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": runtimeRoot}, func() {
				withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
					withProjectFileContents(t, filepath.Join("configs", "kbase-settings.yml"), nil, func() {
						_, err := Load()
						if err == nil || !deprecation.Is(err) || !strings.Contains(err.Error(), legacyDir) {
							t.Fatalf("expected removed runtime directory error, got %v", err)
						}
					})
				})
			})
		})
	}
}

func TestLoadRejectsRemovedSkillsMarketRuntimeDirectoryWithStateOverride(t *testing.T) {
	runtimeRoot := t.TempDir()
	legacyDir := filepath.Join(runtimeRoot, "skills-market")
	if err := os.Mkdir(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": runtimeRoot, "AP_RUNTIME_STATE_DIR": filepath.Join(t.TempDir(), "state")}, func() {
		if _, err := Load(LoadOptions{ConfigDir: t.TempDir()}); err == nil || !deprecation.Is(err) || !strings.Contains(err.Error(), legacyDir) {
			t.Fatalf("expected legacy runtime directory error, got %v", err)
		}
	})
}

func TestLoadRejectsRuntimePathsFromYAML(t *testing.T) {
	for _, key := range []string{"registries-dir", "tools-dir", "owner-dir", "agents-dir", "ru-agents-dir", "teams-dir", "root-dir", "automations-dir", "chats-dir", "memory-dir", "kbase-dir", "pan-dir", "skills-center-dir", "connectors-center-dir", "ru-connectors-dir", "state-dir", "connectors-dir", "connector-state-dir"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "configs"), 0o700); err != nil {
				t.Fatal(err)
			}
			contents := "paths:\n  " + key + ": var/custom-directory\n"
			if err := os.WriteFile(filepath.Join(root, "configs", "runtime.yml"), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": filepath.Join(root, "runtime"), "AP_RUNTIME_STATE_DIR": filepath.Join(root, "env-state")}, func() {
				if _, err := Load(LoadOptions{ConfigDir: root}); err == nil || !deprecation.Is(err) || !strings.Contains(err.Error(), "paths configuration was removed") {
					t.Fatalf("YAML override accepted: %v", err)
				}
			})
		})
	}
	for _, value := range []string{"{}", "null", "[]", "invalid"} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.yml")
			if err := os.WriteFile(path, []byte("paths: "+value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var cfg Config
			if err := cfg.applyRuntimeFile(path); err == nil || !deprecation.Is(err) {
				t.Fatalf("removed paths block accepted: %v", err)
			}
		})
	}
}

func TestRUAgentsDirHasNoDedicatedEnvironmentOverride(t *testing.T) {
	t.Setenv("AP_RUNTIME_RU_AGENTS_DIR", filepath.Join(t.TempDir(), "ignored"))
	withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "kbase-settings.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(cfg.Paths.RUAgentsDir, "ignored") {
				t.Fatalf("unexpected dedicated environment override: %q", cfg.Paths.RUAgentsDir)
			}
		})
	})
}

func TestValidateRUAgentsDirRejectsOverlapAndFilesystemRoot(t *testing.T) {
	root := t.TempDir()
	base := PathsConfig{
		AgentsDir:       filepath.Join(root, "agents"),
		TeamsDir:        filepath.Join(root, "teams"),
		SkillsCenterDir: filepath.Join(root, "skills-center"),
		ChatsDir:        filepath.Join(root, "chats"),
		MemoryDir:       filepath.Join(root, "memory"),
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "same", path: base.AgentsDir, want: "agents-dir"},
		{name: "contains source", path: root, want: "must not overlap"},
		{name: "inside source", path: filepath.Join(base.AgentsDir, "generated"), want: "agents-dir"},
		{name: "filesystem root", path: string(filepath.Separator), want: "filesystem root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paths := base
			paths.RUAgentsDir = tc.path
			err := validateRUAgentsDir(paths)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateRUAgentsDir(%q) = %v, want %q", tc.path, err, tc.want)
			}
		})
	}
}

func TestLoadRuntimeDirAllowsCommonDirectoryOverrides(t *testing.T) {
	panDir := filepath.Join(t.TempDir(), "custom-pan")
	if err := os.Mkdir(panDir, 0o755); err != nil {
		t.Fatalf("make pan dir: %v", err)
	}
	withIsolatedEnv(t, map[string]string{
		"AP_RUNTIME_DIR":            filepath.Join("var", "runtime"),
		"AP_RUNTIME_REGISTRIES_DIR": filepath.Join("var", "custom-registries"),
		"AP_RUNTIME_CHATS_DIR":      filepath.Join("var", "custom-chats"),
		"AP_RUNTIME_MEMORY_DIR":     filepath.Join("var", "custom-memory"),
		"AP_RUNTIME_PAN_DIR":        panDir,
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Paths.RegistriesDir != filepath.Join("var", "custom-registries") {
			t.Fatalf("unexpected registries dir: %q", cfg.Paths.RegistriesDir)
		}
		if cfg.Paths.ChatsDir != filepath.Join("var", "custom-chats") {
			t.Fatalf("unexpected chats dir: %q", cfg.Paths.ChatsDir)
		}
		if cfg.Paths.MemoryDir != filepath.Join("var", "custom-memory") {
			t.Fatalf("unexpected memory dir: %q", cfg.Paths.MemoryDir)
		}
		if cfg.Paths.PanDir != panDir {
			t.Fatalf("unexpected pan dir: %q", cfg.Paths.PanDir)
		}
		if cfg.Providers.ExternalDir != filepath.Join("var", "custom-registries", "providers") {
			t.Fatalf("unexpected providers dir: %q", cfg.Providers.ExternalDir)
		}
		if cfg.Models.ExternalDir != filepath.Join("var", "custom-registries", "models") {
			t.Fatalf("unexpected models dir: %q", cfg.Models.ExternalDir)
		}
	})
}

func TestLoadAcceptsAPEnvAllowlist(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_CHAT_RESOURCE_TICKET_SECRET": "ap-secret",
		"AP_DEBUG_LLM_CONSOLE":           "raw,parsed",
		"AP_DEBUG_LLM_CHAT_RECORD":       "true",
		"AP_CONTAINER_HUB_BASE_URL":      "http://ap-hub",
	}, func() {
		content := "" +
			"container-hub:\n" +
			"  auth-token: runtime-token\n" +
			"  default-environment-id: runtime-env\n" +
			"  request-timeout: 302\n" +
			"  default-sandbox-level: agent\n" +
			"  agent-idle-timeout: 303\n" +
			"  destroy-queue-delay: 304\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.ResourceTicket.Secret != "ap-secret" {
				t.Fatalf("unexpected resource ticket secret: %q", cfg.ResourceTicket.Secret)
			}
			if cfg.ResourceTicket.TTLSeconds != 86400 {
				t.Fatalf("unexpected resource ticket ttl: %d", cfg.ResourceTicket.TTLSeconds)
			}
			if got := strings.Join(cfg.Logging.LLMInteraction.ConsoleCategories, ","); got != "raw,parsed" {
				t.Fatalf("unexpected llm console categories: %q", got)
			}
			if !cfg.Logging.LLMInteraction.RecordEnabled {
				t.Fatalf("expected llm chat record enabled")
			}
			if cfg.ContainerHub.BaseURL != "http://ap-hub" ||
				cfg.ContainerHub.AuthToken != "runtime-token" ||
				cfg.ContainerHub.DefaultEnvironmentID != "runtime-env" {
				t.Fatalf("unexpected container hub identity: %#v", cfg.ContainerHub)
			}
			if cfg.ContainerHub.RequestTimeout != 302 ||
				cfg.ContainerHub.DefaultSandboxLevel != "agent" ||
				cfg.ContainerHub.AgentIdleTimeout != 303 ||
				cfg.ContainerHub.DestroyQueueDelay != 304 {
				t.Fatalf("unexpected container hub runtime settings: %#v", cfg.ContainerHub)
			}
		})
	})
}

func TestLoadContainerHubAndBashConfigFromFiles(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		runtimeExample, err := os.ReadFile(ProjectFile(filepath.Join("configs", "runtime.example.yml")))
		if err != nil {
			t.Fatalf("read runtime example: %v", err)
		}
		toolsExample, err := os.ReadFile(ProjectFile(filepath.Join("configs", "tools.example.yml")))
		if err != nil {
			t.Fatalf("read tools example: %v", err)
		}
		runtimeContent := strings.ReplaceAll(string(runtimeExample), `base-url: ""`, `base-url: "https://docs.test"`)
		toolsContent := string(toolsExample)
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeContent, func() {
			withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &toolsContent, func() {
				cfg, loadErr := Load()
				if loadErr != nil {
					t.Fatalf("load config: %v", loadErr)
				}
				if !cfg.ContainerHub.Enabled {
					t.Fatalf("expected container hub enabled from config file")
				}
				if cfg.ContainerHub.BaseURL == "" {
					t.Fatalf("expected container hub base url")
				}
				if len(cfg.Bash.AllowedCommands) == 0 {
					t.Fatalf("expected bash allowed commands from config file")
				}
				if cfg.Bash.MaxCommandChars <= 0 {
					t.Fatalf("expected bash runtime limits from config file, got %#v", cfg.Bash)
				}
			})
		})
	})
}

func TestLoadAPEnvAndRuntimeYAMLWithToolsYAMLConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_CONTAINER_HUB_BASE_URL": "http://127.0.0.1:18000",
	}, func() {
		content := "" +
			"bash:\n" +
			"  allowed-commands: pwd,echo\n" +
			"  shell-features-enabled: true\n" +
			"  shell-args:\n" +
			"    - -NoProfile\n" +
			"    - -Command\n" +
			"    - \"{{command}}\"\n"
		runtimeConfig := "" +
			"budget:\n" +
			"  hitl:\n" +
			"    timeout: 60\n" +
			"    question:\n" +
			"      timeout: 70\n" +
			"    approval:\n" +
			"      timeout: 75\n" +
			"    form:\n" +
			"      timeout: 76\n" +
			"    plan:\n" +
			"      timeout: 80\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeConfig, func() {
			withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if !cfg.ContainerHub.Enabled {
					t.Fatalf("expected container hub enabled when base url is set")
				}
				if cfg.ContainerHub.BaseURL != "http://127.0.0.1:18000" {
					t.Fatalf("unexpected base url: %q", cfg.ContainerHub.BaseURL)
				}
				if !cfg.Bash.ShellFeaturesEnabled {
					t.Fatalf("expected shell features enabled from yaml")
				}
				if len(cfg.Bash.AllowedCommands) != 2 {
					t.Fatalf("unexpected allowed commands: %#v", cfg.Bash.AllowedCommands)
				}
				if got := strings.Join(cfg.Bash.ShellArgs, "|"); got != "-NoProfile|-Command|{{command}}" {
					t.Fatalf("unexpected shell args: %#v", cfg.Bash.ShellArgs)
				}
				if cfg.Defaults.Budget.Hitl.Timeout != 60 {
					t.Fatalf("unexpected default HITL budget timeout: %d", cfg.Defaults.Budget.Hitl.Timeout)
				}
				if cfg.Defaults.Budget.Hitl.Question.Timeout != 70 ||
					cfg.Defaults.Budget.Hitl.Approval.Timeout != 75 ||
					cfg.Defaults.Budget.Hitl.Form.Timeout != 76 {
					t.Fatalf("unexpected default HITL mode budget timeout: %#v", cfg.Defaults.Budget.Hitl)
				}
			})
		})
	})
}

func TestLoadRunEnvLimits(t *testing.T) {
	content := "run-env:\n" +
		"  max-dynamic-keys: 12\n" +
		"  max-value-bytes: 512\n" +
		"  max-total-bytes: 4096\n" +
		"  deny-keys: [CUSTOM_DENY]\n"
	withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RunEnv.MaxDynamicKeys != 12 || cfg.RunEnv.MaxValueBytes != 512 || cfg.RunEnv.MaxTotalBytes != 4096 {
			t.Fatalf("limits = %#v", cfg.RunEnv)
		}
		if !reflect.DeepEqual(cfg.RunEnv.DenyKeys, []string{"CUSTOM_DENY"}) {
			t.Fatalf("deny keys = %#v", cfg.RunEnv.DenyKeys)
		}
	})
}

func TestLoadRejectsRemovedPlatformControlAuthorization(t *testing.T) {
	for _, field := range []string{"profiles", "bindings"} {
		t.Run(field, func(t *testing.T) {
			content := "platform-control:\n  " + field + ": {}\n"
			if field == "bindings" {
				content = "platform-control:\n  bindings: []\n"
			}
			withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "platform-control was removed") {
					t.Fatalf("Load error = %v", err)
				}
			})
		})
	}
}

func TestLoadBashShellArgsFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"bash:\n" +
			"  shell-executable: powershell.exe\n" +
			"  shell-args:\n" +
			"    - -NoProfile\n" +
			"    - -ExecutionPolicy\n" +
			"    - Bypass\n" +
			"    - -Command\n" +
			"    - \"{{command}}\"\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Bash.ShellExecutable != "powershell.exe" {
				t.Fatalf("unexpected shell executable: %q", cfg.Bash.ShellExecutable)
			}
			if got := strings.Join(cfg.Bash.ShellArgs, "|"); got != "-NoProfile|-ExecutionPolicy|Bypass|-Command|{{command}}" {
				t.Fatalf("unexpected shell args: %#v", cfg.Bash.ShellArgs)
			}
		})
	})
}

func TestAccessPolicyConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"access-policy:\n" +
			"  levels:\n" +
			"    default:\n" +
			"      read-roots:\n" +
			"        - \"@workspace\"\n" +
			"        - \"@chat\"\n" +
			"      write-roots:\n" +
			"        - \"@workspace\"\n" +
			"        - \"@chat\"\n" +
			"      readonly-roots: []\n" +
			"      approvals:\n" +
			"        read-outside-roots: block\n" +
			"        write-outside-roots: hitl\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			level := cfg.AccessPolicy.Levels["default"]
			if strings.Join(level.ReadRoots, ",") != "@workspace,@chat,@temp" {
				t.Fatalf("unexpected read roots: %#v", level.ReadRoots)
			}
			if strings.Join(level.WriteRoots, ",") != "@workspace,@chat,@temp" {
				t.Fatalf("unexpected write roots: %#v", level.WriteRoots)
			}
			if level.Approvals.ReadOutsideRoots != "block" {
				t.Fatalf("unexpected read outside action: %#v", level.Approvals)
			}
		})
	})
}

func TestAccessPolicyNormalizePreservesRootInheritanceIntent(t *testing.T) {
	cfg := normalizeAccessPolicyConfig(AccessPolicyConfig{
		Levels: map[string]AccessPolicyLevelConfig{
			"default": {
				ReadRoots:  []string{"@workspace", "@chat"},
				WriteRoots: []string{"@workspace", "@chat"},
			},
			"auto_approve": {
				Inherit: "default",
			},
			"empty": {
				Inherit:    "default",
				ReadRoots:  []string{},
				WriteRoots: []string{},
			},
		},
	})

	autoLevel := cfg.Levels["auto_approve"]
	if autoLevel.ReadRoots != nil || autoLevel.WriteRoots != nil {
		t.Fatalf("expected inherited level roots to stay nil, got read=%#v write=%#v", autoLevel.ReadRoots, autoLevel.WriteRoots)
	}

	emptyLevel := cfg.Levels["empty"]
	if emptyLevel.ReadRoots == nil || len(emptyLevel.ReadRoots) != 0 {
		t.Fatalf("expected explicit empty read roots to stay empty slice, got %#v", emptyLevel.ReadRoots)
	}
	if emptyLevel.WriteRoots == nil || len(emptyLevel.WriteRoots) != 0 {
		t.Fatalf("expected explicit empty write roots to stay empty slice, got %#v", emptyLevel.WriteRoots)
	}
}

func TestFileToolsConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"file-tools:\n" +
			"  max-read-bytes: 1234\n" +
			"  max-write-bytes: 5678\n" +

			"  require-write-approval: false\n" +
			"  require-read-before-write: false\n" +
			"  read-before-write-scope: chat\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.FileTools.MaxReadBytes != 1234 || cfg.FileTools.MaxWriteBytes != 5678 {
				t.Fatalf("unexpected file limits: %#v", cfg.FileTools)
			}
			if cfg.FileTools.RequireWriteApproval {
				t.Fatalf("expected write approval disabled from yaml")
			}
			if cfg.FileTools.RequireReadBeforeWrite {
				t.Fatalf("expected read-before-write disabled from yaml")
			}
			if cfg.FileTools.ReadBeforeWriteScope != "chat" {
				t.Fatalf("expected chat read-before-write scope, got %q", cfg.FileTools.ReadBeforeWriteScope)
			}
		})
	})
}

func TestRemovedFileBatchLimitFailsExplicitly(t *testing.T) {
	var cfg Config
	if err := cfg.applyFileToolsValues("tools.yml", map[string]any{"max-batch-ops": 20}); err == nil || !strings.Contains(err.Error(), "max-batch-ops was removed") {
		t.Fatalf("obsolete limit accepted: %v", err)
	}
}

func TestFileToolsConfigRejectsInvalidReadBeforeWriteScope(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "file-tools:\n  read-before-write-scope: global\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "read-before-write-scope") {
				t.Fatalf("expected invalid read-before-write-scope error, got %v", err)
			}
		})
	})
}

func TestToolsConfigRejectsRemovedWorkingDirectoryKeys(t *testing.T) {
	for _, section := range []string{"access-policy", "bash", "file-tools"} {
		t.Run(section, func(t *testing.T) {
			withIsolatedEnv(t, nil, func() {
				content := section + ":\n  working-directory: \"@workspace\"\n"
				withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
					_, err := Load()
					if err == nil || !strings.Contains(err.Error(), section+".working-directory was removed") {
						t.Fatalf("expected removed working-directory error for %s, got %v", section, err)
					}
				})
			})
		})
	}
}

func TestToolsConfigRejectsRemovedPathPolicyKeys(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "bash allowed paths",
			content: "bash:\n  allowed-paths: .\n",
			want:    "bash.allowed-paths",
		},
		{
			name:    "bash path checked commands",
			content: "bash:\n  path-checked-commands: ls\n",
			want:    "bash.path-checked-commands",
		},
		{
			name:    "bash path check bypass commands",
			content: "bash:\n  path-check-bypass-commands: pwd\n",
			want:    "bash.path-check-bypass-commands",
		},
		{
			name:    "file tools read paths",
			content: "file-tools:\n  allowed-read-paths: .\n",
			want:    "file-tools.allowed-read-paths",
		},
		{
			name:    "file tools write paths",
			content: "file-tools:\n  allowed-write-paths: .\n",
			want:    "file-tools.allowed-write-paths",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withIsolatedEnv(t, nil, func() {
				withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &tc.content, func() {
					_, err := Load()
					if err == nil {
						t.Fatalf("expected removed path policy key error")
					}
					if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "access-policy") {
						t.Fatalf("expected error to mention %q and access-policy, got %v", tc.want, err)
					}
				})
			})
		})
	}
}

func TestFileToolsConfigLSPHookYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"file-tools:\n" +
			"  hooks:\n" +
			"    after-file-change:\n" +
			"      lsp-diagnostics:\n" +
			"        enabled: false\n" +
			"        timeout: 42\n" +
			"        languages: [\"go\", \"python\"]\n" +
			"        servers:\n" +
			"          go:\n" +
			"            command: custom-gopls\n" +
			"            args: [\"serve\"]\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			lsp := cfg.FileTools.Hooks.AfterFileChange.LSPDiagnostics
			if lsp.Enabled {
				t.Fatalf("expected lsp diagnostics hook disabled from yaml")
			}
			if lsp.Timeout != 42 {
				t.Fatalf("unexpected timeout: %d", lsp.Timeout)
			}
			if strings.Join(lsp.Languages, ",") != "go,python" {
				t.Fatalf("unexpected languages: %#v", lsp.Languages)
			}
			if got := lsp.Servers["go"]; got.Command != "custom-gopls" || strings.Join(got.Args, ",") != "serve" {
				t.Fatalf("unexpected go server: %#v", got)
			}
			if got := lsp.Servers["typescript"]; got.Command != "typescript-language-server" {
				t.Fatalf("expected default typescript server to remain, got %#v", got)
			}
		})
	})
}

func TestToolsConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"access-policy:\n" +
			"  levels:\n" +
			"    default:\n" +
			"      read-roots:\n" +
			"        - \"@workspace\"\n" +
			"      write-roots:\n" +
			"        - \"@workspace\"\n" +
			"      readonly-roots: []\n" +
			"      approvals:\n" +
			"        read-outside-roots: block\n" +
			"        write-outside-roots: hitl\n" +
			"bash:\n" +
			"  allowed-commands: pwd,echo\n" +
			"  shell-features-enabled: true\n" +
			"  shell-executable: bash\n" +
			"  max-command-chars: 4321\n" +
			"file-tools:\n" +
			"  max-read-bytes: 1234\n" +
			"  max-write-bytes: 5678\n" +

			"  require-write-approval: false\n" +
			"  require-read-before-write: false\n" +
			"  read-before-write-scope: chat\n"
		withProjectFileContents(t, filepath.Join("configs", "access-policy.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "bash.yml"), nil, func() {
				withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), nil, func() {
					withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
						cfg, err := Load()
						if err != nil {
							t.Fatalf("load config: %v", err)
						}
						level := cfg.AccessPolicy.Levels["default"]
						if strings.Join(level.ReadRoots, ",") != "@workspace,@temp" {
							t.Fatalf("unexpected read roots: %#v", level.ReadRoots)
						}
						if level.Approvals.ReadOutsideRoots != "block" {
							t.Fatalf("unexpected read outside action: %#v", level.Approvals)
						}
						if cfg.Bash.ShellExecutable != "bash" || cfg.Bash.MaxCommandChars != 4321 {
							t.Fatalf("unexpected bash config: %#v", cfg.Bash)
						}
						if strings.Join(cfg.Bash.AllowedCommands, ",") != "pwd,echo" {
							t.Fatalf("unexpected allowed commands: %#v", cfg.Bash.AllowedCommands)
						}
						if cfg.FileTools.MaxReadBytes != 1234 || cfg.FileTools.MaxWriteBytes != 5678 {
							t.Fatalf("unexpected file limits: %#v", cfg.FileTools)
						}
						if cfg.FileTools.RequireWriteApproval || cfg.FileTools.RequireReadBeforeWrite {
							t.Fatalf("expected file approval flags disabled from yaml, got %#v", cfg.FileTools)
						}
						if cfg.FileTools.ReadBeforeWriteScope != "chat" {
							t.Fatalf("expected chat read-before-write scope, got %q", cfg.FileTools.ReadBeforeWriteScope)
						}
					})
				})
			})
		})
	})
}

func TestLoadContainerHubDisabledWhenBaseURLMissing(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"auth-token:\n" +
			"default-environment-id:\n" +
			"request-timeout: 300\n" +
			"default-sandbox-level: run\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "container-hub.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.ContainerHub.Enabled {
					t.Fatalf("expected container hub disabled when base url is missing")
				}
				if cfg.ContainerHub.BaseURL != "" {
					t.Fatalf("expected empty base url, got %q", cfg.ContainerHub.BaseURL)
				}
			})
		})
	})
}

func TestLoadIgnoresLLMInteractionRuntimeYAML(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE": "true",
	}, func() {
		content := "" +
			"logging:\n" +
			"  llm-interaction:\n" +
			"    enabled: false\n" +
			"    console-categories: [raw, parsed]\n" +
			"    mask-sensitive: true\n" +
			"    record-enabled: true\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !cfg.Logging.LLMInteraction.Enabled {
				t.Fatalf("expected llm interaction logging to keep source default enabled")
			}
			if got := strings.Join(cfg.Logging.LLMInteraction.ConsoleCategories, ","); got != "request,usage" {
				t.Fatalf("expected llm interaction console categories to keep source default, got %q", got)
			}
			if cfg.Logging.LLMInteraction.MaskSensitive {
				t.Fatalf("expected runtime yaml llm interaction mask-sensitive config to be ignored")
			}
			if cfg.Logging.LLMInteraction.RecordEnabled {
				t.Fatalf("expected runtime yaml llm interaction record-enabled config to be ignored")
			}
		})
	})
}

func TestLoadLLMConsoleFromAPDebugEnv(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "raw and parsed", env: "raw,parsed", want: "raw,parsed"},
		{name: "none", env: "none", want: "none"},
		{name: "all", env: "all", want: "all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIsolatedEnv(t, map[string]string{
				"AP_DEBUG_LLM_CONSOLE": tt.env,
			}, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if got := strings.Join(cfg.Logging.LLMInteraction.ConsoleCategories, ","); got != tt.want {
					t.Fatalf("expected llm console categories %q, got %q", tt.want, got)
				}
			})
		})
	}
}

func TestLoadLLMChatRecordFromAPDebugEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_DEBUG_LLM_CHAT_RECORD": "true",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Logging.LLMInteraction.RecordEnabled {
			t.Fatalf("expected llm chat record enabled from env")
		}
		if cfg.Logging.LLMInteraction.RecordDir != filepath.Join("runtime", "chats") {
			t.Fatalf("unexpected llm chat record dir: %q", cfg.Logging.LLMInteraction.RecordDir)
		}
	})
}

func TestGatewaysEmptyWhenNoChannelsConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{}, func() {
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if len(cfg.Gateways) != 0 {
				t.Fatalf("expected empty Gateways when no channel config, got %d", len(cfg.Gateways))
			}
		})
	})
}

func TestLoadChannelsConfigRejectsRemovedLegacyFields(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"WECOM_BRIDGE_WS_URL":    "wss://bridge.example.com/ws/agent?channel=wecom:corp1",
		"WECOM_BRIDGE_JWT_TOKEN": "jwt-wecom",
	}, func() {
		content := "" +
			"channels:\n" +
			"  wecom:\n" +
			"    type: bridge\n" +
			"    default-agent: customer-service\n" +
			"    agents: \"*\"\n" +
			"    gateway:\n" +
			"      url: ${WECOM_BRIDGE_WS_URL}\n" +
			"      jwt-token: ${WECOM_BRIDGE_JWT_TOKEN}\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "does not support key") {
				t.Fatalf("legacy channel config must fail, got %v", err)
			}
		})
	})
}

func TestParseChannelConfigRejectsEveryRemovedTopLevelKey(t *testing.T) {
	for _, key := range []string{"type", "default-agent", "agents", "gateway"} {
		t.Run(key, func(t *testing.T) {
			if _, err := parseChannelConfig("peer", map[string]any{key: "legacy"}); err == nil || !strings.Contains(err.Error(), "does not support key") {
				t.Fatalf("removed key %q must fail, got %v", key, err)
			}
		})
	}
}

func TestParseChannelConfigRejectsKeysOutsideCanonicalSchema(t *testing.T) {
	for _, key := range []string{"name", "unexpected"} {
		t.Run(key, func(t *testing.T) {
			if _, err := parseChannelConfig("peer", map[string]any{key: "value"}); err == nil || !strings.Contains(err.Error(), "only mode, transport, protocol, endpoint, auth, heartbeat, and reconnect are accepted") {
				t.Fatalf("unsupported key %q must fail, got %v", key, err)
			}
		})
	}
}

func TestLoadChannelsConfigCanonicalClientServerDefaults(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"PEER_A_TOKEN": "peer-token",
	}, func() {
		content := "" +
			"channels:\n" +
			"  peer-a:\n" +
			"    mode: client\n" +
			"    endpoint:\n" +
			"      url: ws://peer-a.example.com/ws/channel?channelId=peer-a\n" +
			"      tokenEnv: PEER_A_TOKEN\n" +
			"    heartbeat:\n" +
			"      interval: 20\n" +
			"    reconnect:\n" +
			"      handshakeTimeout: 7\n" +
			"      min: 2\n" +
			"      max: 40\n" +
			"  public-entry:\n" +
			"    mode: server\n" +
			"    endpoint:\n" +
			"      path: /ws/channel\n" +
			"    auth:\n" +
			"      type: jwt\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if len(cfg.Channels) != 2 {
				t.Fatalf("expected 2 channels, got %d", len(cfg.Channels))
			}
			byID := map[string]ChannelConfig{}
			for _, ch := range cfg.Channels {
				byID[ch.ID] = ch
			}
			peer := byID["peer-a"]
			if peer.Mode != ChannelModeClient || peer.Transport != ChannelTransportWebSocket || peer.Protocol != ChannelProtocolPlatformWS {
				t.Fatalf("unexpected peer defaults: %#v", peer)
			}
			if peer.Endpoint.URL != "ws://peer-a.example.com/ws/channel?channelId=peer-a" || peer.Endpoint.Token != "peer-token" {
				t.Fatalf("unexpected peer endpoint: %#v", peer.Endpoint)
			}
			if peer.Heartbeat.Interval != 20 || peer.Reconnect.HandshakeTimeout != 7 || peer.Reconnect.Min != 2 || peer.Reconnect.Max != 40 {
				t.Fatalf("unexpected peer connection policy: heartbeat=%#v reconnect=%#v", peer.Heartbeat, peer.Reconnect)
			}
			publicEntry := byID["public-entry"]
			if publicEntry.Mode != ChannelModeServer || publicEntry.Transport != ChannelTransportWebSocket || publicEntry.Protocol != ChannelProtocolPlatformWS {
				t.Fatalf("unexpected server defaults: %#v", publicEntry)
			}
			if publicEntry.Endpoint.Path != "/ws/channel" || publicEntry.Auth.Type != "jwt" {
				t.Fatalf("unexpected public entry config: %#v", publicEntry)
			}
			if len(cfg.Gateways) != 1 {
				t.Fatalf("expected only client channel to synthesize gateway, got %#v", cfg.Gateways)
			}
			if cfg.Gateways[0].ID != "peer-a" || cfg.Gateways[0].Channel != "peer-a" ||
				cfg.Gateways[0].URL != peer.Endpoint.URL || cfg.Gateways[0].JwtToken != "peer-token" {
				t.Fatalf("unexpected synthesized gateway: %#v", cfg.Gateways[0])
			}
			if cfg.Gateways[0].HandshakeTimeout != 7 || cfg.Gateways[0].ReconnectMin != 2 || cfg.Gateways[0].ReconnectMax != 40 {
				t.Fatalf("unexpected synthesized gateway reconnect policy: %#v", cfg.Gateways[0])
			}
		})
	})
}

func TestLoadChannelsConfigAllowsCustomChannelIDForWecomSource(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  company-gateway:\n" +
			"    mode: client\n" +
			"    endpoint:\n" +
			"      url: ws://example-gateway.local/ws/agent?agentKey=demo-agent&channel=wecom:langyage\n" +
			"      token: token\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if len(cfg.Gateways) != 1 {
				t.Fatalf("expected one gateway, got %d", len(cfg.Gateways))
			}
			gateway := cfg.Gateways[0]
			if gateway.ID != "company-gateway" || gateway.Channel != "company-gateway" {
				t.Fatalf("expected user channel id to be preserved, got %#v", gateway)
			}
			if gateway.SourceChannel != "wecom:langyage" || gateway.SourcePrefix != "wecom" {
				t.Fatalf("expected wecom source route to be derived, got %#v", gateway)
			}
		})
	})
}

func TestLoadChannelsConfigRejectsInvalidType(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  wecom:\n" +
			"    type: invalid\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "does not support key") {
				t.Fatalf("expected removed channel type to fail")
			}
		})
	})
}

func TestLoadChannelsConfigRejectsGatewayConflicts(t *testing.T) {
	cfg := defaultConfig(LoadOptions{})
	cfg.Gateways = []GatewayEntry{{
		ID:      "existing",
		Channel: "wecom",
		URL:     "ws://existing.example.com/ws/agent?channel=wecom:corp1",
	}}
	cfg.Channels = []ChannelConfig{
		{
			ID:       "wecom",
			Mode:     ChannelModeClient,
			Endpoint: ChannelEndpointConfig{URL: "ws://bridge.example.com/ws/agent?channel=wecom:corp1"},
		},
	}
	if err := cfg.normalize(""); err == nil {
		t.Fatalf("expected duplicate channel gateway conflict to fail")
	}
}

func TestLoadChannelsConfigRejectsMissingEndpointURL(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  mobile:\n" +
			"    mode: client\n" +
			"    endpoint:\n" +
			"      token: token\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			if _, err := Load(); err == nil {
				t.Fatalf("expected missing endpoint url to fail")
			}
		})
	})
}

func TestLoadGatewayConfigFromChannels(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  mobile:\n" +
			"    mode: client\n" +
			"    endpoint:\n" +
			"      url: ws://127.0.0.1:17999/gw?channel=mobile\n" +
			"      token: jwt-abc\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if len(cfg.Gateways) != 1 {
				t.Fatalf("expected one gateway from channels config, got %d", len(cfg.Gateways))
			}
			if cfg.Gateways[0].URL != "ws://127.0.0.1:17999/gw?channel=mobile" {
				t.Fatalf("unexpected gateway url: %q", cfg.Gateways[0].URL)
			}
		})
	})
}

func TestLoadFailsWhenExplicitPanDirDoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-pan")
	withIsolatedEnv(t, map[string]string{
		"AP_RUNTIME_PAN_DIR": missing,
	}, func() {
		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail for missing AP_RUNTIME_PAN_DIR")
		}
		if !strings.Contains(err.Error(), "AP_RUNTIME_PAN_DIR does not exist: "+missing) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadFailsWhenExplicitPanDirIsFile(t *testing.T) {
	panFile := filepath.Join(t.TempDir(), "pan-file")
	if err := os.WriteFile(panFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write pan file: %v", err)
	}
	withIsolatedEnv(t, map[string]string{
		"AP_RUNTIME_PAN_DIR": panFile,
	}, func() {
		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail for file AP_RUNTIME_PAN_DIR")
		}
		if !strings.Contains(err.Error(), "AP_RUNTIME_PAN_DIR is not a directory: "+panFile) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func withIsolatedEnv(t *testing.T, values map[string]string, fn func()) {
	t.Helper()

	keys := []string{
		"AP_RUNTIME_DIR",
		"AP_RUNTIME_STATE_DIR",
		"SERVER_PORT",
		"AP_RUNTIME_REGISTRIES_DIR",
		"OWNER_DIR",
		"AGENTS_DIR",
		"TEAMS_DIR",
		"ROOT_DIR",
		"AUTOMATIONS_DIR",
		"AP_RUNTIME_CHATS_DIR",
		"AP_RUNTIME_MEMORY_DIR",
		"AP_RUNTIME_PAN_DIR",
		"TOOLS_DIR",
		"AP_CONTAINER_HUB_BASE_URL",
		"AP_CONTAINER_HUB_AUTH_TOKEN",
		"AP_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID",
		"AP_CONTAINER_HUB_REQUEST_TIMEOUT",
		"AP_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL",
		"AP_CONTAINER_HUB_AGENT_IDLE_TIMEOUT",
		"AP_CONTAINER_HUB_DESTROY_QUEUE_DELAY",
		"CONTAINER_HUB_BASE_URL",
		"CONTAINER_HUB_AUTH_TOKEN",
		"CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID",
		"CONTAINER_HUB_REQUEST_TIMEOUT",
		"CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL",
		"CONTAINER_HUB_AGENT_IDLE_TIMEOUT",
		"CONTAINER_HUB_DESTROY_QUEUE_DELAY",
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
		"AP_AUTH_ENABLED",
		"AP_AUTH_JWKS_URI",
		"AP_AUTH_ISSUER",
		"AP_AUTH_JWKS_CACHE_SECONDS",
		"AUTH_ENABLED",
		"AUTH_JWKS_URI",
		"AUTH_ISSUER",
		"AUTH_JWKS_CACHE_SECONDS",
		"AP_CHAT_RESOURCE_TICKET_SECRET",
		"CHAT_RESOURCE_TICKET_SECRET",
		"AGENT_H2A_RENDER_FLUSH_INTERVAL_MS",
		"AGENT_H2A_RENDER_MAX_BUFFERED_CHARS",
		"AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS",
		"AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH",
		"AGENT_AUTOMATION_ENABLED",
		"AGENT_AUTOMATION_DEFAULT_ZONE_ID",
		"AGENT_AUTOMATION_POOL_SIZE",
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
		"AGENT_DEFAULT_MAX_OUTPUT_TOKENS",
		"AGENT_DEFAULT_BUDGET_MAX_STEPS",
		"AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT",
		"AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS",
		"AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT",
		"BUDGET_HITL_TIMEOUT",
		"BUDGET_HITL_QUESTION_TIMEOUT",
		"BUDGET_HITL_APPROVAL_TIMEOUT",
		"BUDGET_HITL_FORM_TIMEOUT",
		"BUDGET_HITL_PLAN_TIMEOUT",
		"LOGGING_AGENT_REQUEST_ENABLED",
		"LOGGING_AGENT_AUTH_ENABLED",
		"LOGGING_AGENT_EXCEPTION_ENABLED",
		"LOGGING_AGENT_TOOL_ENABLED",
		"LOGGING_AGENT_ACTION_ENABLED",
		"LOGGING_AGENT_VIEWPORT_ENABLED",
		"LOGGING_AGENT_SSE_ENABLED",
		"LOGGING_AGENT_LLM_INTERACTION_ENABLED",
		"LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE",
		"AP_DEBUG_LLM_CONSOLE",
		"AP_DEBUG_LLM_CHAT_RECORD",
		"DEBUG_LLM_CONSOLE",
		"DEBUG_LLM_CHAT_RECORD",
		"AGENT_GATEWAY_WS_URL",
		"GATEWAY_WS_URL",
		"GATEWAY_JWT_TOKEN",
		"GATEWAY_BASE_URL",
		"AGENT_GATEWAY_WS_HANDSHAKE_TIMEOUT_MS",
		"AGENT_GATEWAY_WS_RECONNECT_MIN_MS",
		"AGENT_GATEWAY_WS_RECONNECT_MAX_MS",
	}
	for key := range values {
		keys = append(keys, key)
	}
	seenKeys := map[string]struct{}{}
	uniqueKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := seenKeys[key]; ok {
			continue
		}
		seenKeys[key] = struct{}{}
		uniqueKeys = append(uniqueKeys, key)
	}
	keys = uniqueKeys

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

func withProjectFileContents(t *testing.T, relativePath string, content *string, fn func()) {
	t.Helper()

	path := ProjectFile(relativePath)
	original, err := os.ReadFile(path)
	originalExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", path, err)
	}

	if content == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", path, err)
		}
	} else {
		if err := os.WriteFile(path, []byte(*content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	t.Cleanup(func() {
		if originalExists {
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatalf("restore %s: %v", path, err)
			}
			return
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("cleanup %s: %v", path, err)
		}
	})

	fn()
}

func TestNewApprovalKeysUseLevelDefaultsAndInheritance(t *testing.T) {
	cfg := normalizeAccessPolicyConfig(AccessPolicyConfig{Levels: map[string]AccessPolicyLevelConfig{
		"default":      {Approvals: AccessPolicyApprovalConfig{ReadOutsideRoots: "hitl"}},
		"full_access":  {ReadRoots: []string{"@root"}, WriteRoots: []string{"@root"}},
		"auto_approve": {Inherit: "default"},
	}})
	if got := cfg.Levels["default"].Approvals; got.Destructive != "hitl" || got.ExecutableConfig != "hitl" || got.RemoteMutation != "hitl" {
		t.Fatalf("default level: %#v", got)
	}
	if got := cfg.Levels["full_access"].Approvals; got.Destructive != "allow" || got.RemoteMutation != "allow" || got.ExecutableConfig != "allow" {
		t.Fatalf("an older full_access block keeps full semantics: %#v", got)
	}
	if got := cfg.Levels["auto_approve"].Approvals; got.Destructive != "" {
		t.Fatalf("inheriting levels resolve new keys from their parent: %#v", got)
	}
	for _, root := range DefaultAccessPolicyConfig().Levels["default"].WriteRoots {
		if root == "@workspace" {
			t.Fatal("workspace writes are governed by editing, not write-roots")
		}
	}
}

func TestRunEnvHardCutConfiguration(t *testing.T) {
	for _, field := range []string{"deny-keys", "max-dynamic-keys", "max-value-bytes", "max-total-bytes"} {
		content := "platform-control:\n  " + field + ": 12\nrun-env:\n  max-dynamic-keys: 32\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "platform-control was removed") {
				t.Fatalf("legacy %s: %v", field, err)
			}
		})
	}
	content := "run-env:\n  enabled: false\n"
	withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "unknown run-env.enabled") {
			t.Fatal(err)
		}
	})
	content = "run-env:\n  max-dynamic-keys: 7\n"
	withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
		cfg, err := Load()
		if err != nil || cfg.RunEnv.MaxDynamicKeys != 7 {
			t.Fatalf("independence %#v %v", cfg.RunEnv, err)
		}
	})
}

func TestRejectRemovedAgentIndexRuntimeDirectory(t *testing.T) {
	t.Setenv("AP_RUNTIME_KBASE_DIR", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AP_RUNTIME_KBASE_DIR was removed") {
		t.Fatalf("legacy directory must fail even if empty: %v", err)
	}
}
