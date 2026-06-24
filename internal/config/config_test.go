package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		runtimeConfig := "" +
			"desktop:\n" +
			"  action:\n" +
			"    host: 127.0.0.1\n" +
			"    port: 11788\n" +
			"    path: /actions/call\n" +
			"    request-timeout: 20\n" +
			"  cdp:\n" +
			"    host: 127.0.0.1\n" +
			"    port: 11788\n" +
			"    path: /cdp/call\n" +
			"    request-timeout: 20\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeConfig, func() {
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
			if cfg.Billing.Currency != "CNY" {
				t.Fatalf("expected default billing currency CNY, got %q", cfg.Billing.Currency)
			}
			if cfg.SSE.HeartbeatInterval != 30 {
				t.Fatalf("expected default heartbeat interval 30, got %d", cfg.SSE.HeartbeatInterval)
			}
			if !cfg.Logging.Request.Enabled ||
				!cfg.Logging.Auth.Enabled ||
				!cfg.Logging.Exception.Enabled ||
				!cfg.Logging.Tool.Enabled ||
				!cfg.Logging.Action.Enabled ||
				!cfg.Logging.Viewport.Enabled ||
				!cfg.Logging.Memory.Enabled ||
				!cfg.Logging.LLMInteraction.Enabled {
				t.Fatalf("expected default logging surfaces enabled, got %#v", cfg.Logging)
			}
			if cfg.Logging.SSE.Enabled {
				t.Fatalf("expected sse logging disabled by default")
			}
			if cfg.Logging.Memory.File != filepath.Join("runtime", "memory", "memory.log") {
				t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
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
				cfg.Defaults.Budget.Hitl.Form.Timeout != 0 || cfg.Defaults.Budget.Hitl.Plan.Timeout != 0 {
				t.Fatalf("expected default HITL mode timeouts unset, got %#v", cfg.Defaults.Budget.Hitl)
			}
			if cfg.Defaults.Budget.Timeout != 3600 {
				t.Fatalf("expected default budget timeout 3600, got %d", cfg.Defaults.Budget.Timeout)
			}
			if cfg.Defaults.Budget.Model.Timeout != 60 {
				t.Fatalf("expected default model timeout 60, got %d", cfg.Defaults.Budget.Model.Timeout)
			}
			if cfg.Defaults.Budget.Model.MaxCalls != 100 {
				t.Fatalf("expected default model max calls 100, got %d", cfg.Defaults.Budget.Model.MaxCalls)
			}
			if cfg.Defaults.Budget.Model.RetryCount != 3 {
				t.Fatalf("expected default model retry count 3, got %d", cfg.Defaults.Budget.Model.RetryCount)
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
				t.Fatalf("expected memory runtime enabled by default")
			}
			if cfg.Desktop.Action.BridgeURL != "http://127.0.0.1:11788/actions/call" {
				t.Fatalf("unexpected default desktop action bridge url: %q", cfg.Desktop.Action.BridgeURL)
			}
			if cfg.Desktop.CDP.BridgeURL != "http://127.0.0.1:11788/cdp/call" {
				t.Fatalf("unexpected default desktop cdp bridge url: %q", cfg.Desktop.CDP.BridgeURL)
			}
			defaultLevel := cfg.AccessPolicy.Levels["default"]
			if got := strings.Join(defaultLevel.ReadRoots, ","); got != "@workspace,@chat,@agent,@skills" {
				t.Fatalf("unexpected default access-policy read roots: %#v", defaultLevel.ReadRoots)
			}
			if got := strings.Join(defaultLevel.WriteRoots, ","); got != "@workspace,@chat" {
				t.Fatalf("unexpected default access-policy write roots: %#v", defaultLevel.WriteRoots)
			}
		})
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

func TestLoadDesktopConfigMissingFileLeavesBridgeUnconfigured(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "desktop.yml"), nil, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.Desktop.Action.BridgeURL != "" {
					t.Fatalf("expected missing desktop action bridge url, got %q", cfg.Desktop.Action.BridgeURL)
				}
				if cfg.Desktop.CDP.BridgeURL != "" {
					t.Fatalf("expected missing desktop cdp bridge url, got %q", cfg.Desktop.CDP.BridgeURL)
				}
			})
		})
	})
}

func TestLoadDesktopConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"desktop:\n" +
			"  action:\n" +
			"    host: 127.0.0.2\n" +
			"    port: 17001\n" +
			"    path: actions/custom\n" +
			"    request-timeout: 12\n" +
			"  cdp:\n" +
			"    host: localhost\n" +
			"    port: 17002\n" +
			"    path: /cdp/custom\n" +
			"    request-timeout: 56\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Desktop.Action.BridgeURL != "http://127.0.0.2:17001/actions/custom" {
				t.Fatalf("unexpected desktop action bridge url: %q", cfg.Desktop.Action.BridgeURL)
			}
			if cfg.Desktop.Action.RequestTimeout != 12 {
				t.Fatalf("unexpected desktop action timeout: %d", cfg.Desktop.Action.RequestTimeout)
			}
			if cfg.Desktop.CDP.BridgeURL != "http://localhost:17002/cdp/custom" {
				t.Fatalf("unexpected desktop cdp bridge url: %q", cfg.Desktop.CDP.BridgeURL)
			}
			if cfg.Desktop.CDP.RequestTimeout != 56 {
				t.Fatalf("unexpected desktop cdp timeout: %d", cfg.Desktop.CDP.RequestTimeout)
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
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n" +
			"  auth-token: runtime-token\n" +
			"  default-environment-id: runtime-env\n" +
			"  request-timeout: 123\n" +
			"  default-sandbox-level: agent\n" +
			"  agent-idle-timeout: 654321\n" +
			"  destroy-queue-delay: 2345\n" +
			"desktop:\n" +
			"  action:\n" +
			"    host: 127.0.0.3\n" +
			"    port: 17101\n" +
			"    path: actions/runtime\n" +
			"    request-timeout: 23\n" +
			"  cdp:\n" +
			"    host: localhost\n" +
			"    port: 17102\n" +
			"    path: /cdp/runtime\n" +
			"    request-timeout: 67\n" +
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
			withProjectFileContents(t, filepath.Join("configs", "desktop.yml"), nil, func() {
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
						if cfg.Desktop.Action.BridgeURL != "http://127.0.0.3:17101/actions/runtime" || cfg.Desktop.Action.RequestTimeout != 23 {
							t.Fatalf("unexpected desktop action config: %#v", cfg.Desktop.Action)
						}
						if cfg.Desktop.CDP.BridgeURL != "http://localhost:17102/cdp/runtime" || cfg.Desktop.CDP.RequestTimeout != 67 {
							t.Fatalf("unexpected desktop cdp config: %#v", cfg.Desktop.CDP)
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
							cfg.Defaults.Budget.Hitl.Form.Timeout != 640 ||
							cfg.Defaults.Budget.Hitl.Plan.Timeout != 650 {
							t.Fatalf("unexpected runtime HITL budget config: %#v", cfg.Defaults.Budget.Hitl)
						}
					})
				})
			})
		})
	})
}

func TestLoadIgnoresResourceTicketTTLEnv(t *testing.T) {
	apTTLKey := strings.Join([]string{"AP", "CHAT", "RESOURCE", "TICKET", "TTL", "SECONDS"}, "_")
	legacyTTLKey := strings.Join([]string{"CHAT", "RESOURCE", "TICKET", "TTL", "SECONDS"}, "_")
	withIsolatedEnv(t, map[string]string{
		apTTLKey:     "301",
		legacyTTLKey: "401",
	}, func() {
		content := "" +
			"resource:\n" +
			"  ticket-ttl-seconds: 123\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.ResourceTicket.TTLSeconds != 123 {
				t.Fatalf("expected resource ticket ttl to come from runtime.yml, got %d", cfg.ResourceTicket.TTLSeconds)
			}
		})
	})
}

func TestLoadIgnoresLegacyBudgetHITLEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"BUDGET_HITL_TIMEOUT":          "710",
		"BUDGET_HITL_QUESTION_TIMEOUT": "720",
		"BUDGET_HITL_APPROVAL_TIMEOUT": "730",
		"BUDGET_HITL_FORM_TIMEOUT":     "740",
		"BUDGET_HITL_PLAN_TIMEOUT":     "750",
	}, func() {
		content := "" +
			"budget:\n" +
			"  hitl:\n" +
			"    timeout: 610\n" +
			"    question:\n" +
			"      timeout: 620\n" +
			"    approval:\n" +
			"      timeout: 630\n" +
			"    form:\n" +
			"      timeout: 640\n" +
			"    plan:\n" +
			"      timeout: 650\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Defaults.Budget.Hitl.Timeout != 610 ||
				cfg.Defaults.Budget.Hitl.Question.Timeout != 620 ||
				cfg.Defaults.Budget.Hitl.Approval.Timeout != 630 ||
				cfg.Defaults.Budget.Hitl.Form.Timeout != 640 ||
				cfg.Defaults.Budget.Hitl.Plan.Timeout != 650 {
				t.Fatalf("expected runtime yaml HITL budget config to win, got %#v", cfg.Defaults.Budget.Hitl)
			}
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

func TestLoadIgnoresLegacyContainerHubBaseURLEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"CONTAINER_HUB_BASE_URL": "http://legacy-env-hub",
	}, func() {
		content := "" +
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.ContainerHub.BaseURL != "http://runtime-hub" {
				t.Fatalf("expected legacy container hub env to be ignored, got %q", cfg.ContainerHub.BaseURL)
			}
		})
	})
}

func TestLoadPromptsConfigLeavesSkillInstructionsEmptyWhenFileMissing(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Prompts.Skill.InstructionsPrompt != "" {
				t.Fatalf("expected empty prompts override when file is missing, got %q", cfg.Prompts.Skill.InstructionsPrompt)
			}
		})
	})
}

func TestLoadPromptsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"skill:\n" +
			"  catalog-header: custom skills header\n" +
			"  disclosure-header: custom disclosure\n" +
			"  instructions-label: custom label\n" +
			"  instructions-prompt: |\n" +
			"    custom skill instructions\n" +
			"    second line\n" +
			"tool-appendix:\n" +
			"  tool-description-title: custom tool title\n" +
			"  after-call-hint-title: custom hint title\n" +
			"plan-execute:\n" +
			"  task-execution-prompt-template: |\n" +
			"    custom task {{task_id}}\n" +
			"  plan-user-prompt-template: |\n" +
			"    custom plan {{user_request}}\n" +
			"  summary-system-prompt: custom summary system\n" +
			"  summary-user-prompt-template: |\n" +
			"    custom summary {{task_results}}\n" +
			"coder:\n" +
			"  system-prompt: |\n" +
			"    custom coder system\n" +
			"    read before editing\n" +
			"  planning-prompt: |\n" +
			"    custom coder planning\n" +
			"    use finalize_planning only\n" +
			"  summary-system-prompt: custom coder summary system\n" +
			"  summary-user-prompt-template: |\n" +
			"    custom coder summary {{confirmed_plan}}\n" +
			"memory:\n" +
			"  system-prompt-template: |\n" +
			"    custom memory system\n" +
			"    {{task_instruction}}\n" +
			"  user-prompt-template: |\n" +
			"    custom memory user\n" +
			"    {{source_text}}\n"
		withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			want := "custom skill instructions\nsecond line"
			if cfg.Prompts.Skill.InstructionsPrompt != want {
				t.Fatalf("expected prompts override %q, got %q", want, cfg.Prompts.Skill.InstructionsPrompt)
			}
			if cfg.Prompts.Skill.CatalogHeader != "custom skills header" {
				t.Fatalf("expected catalog header override, got %q", cfg.Prompts.Skill.CatalogHeader)
			}
			if cfg.Prompts.Skill.DisclosureHeader != "custom disclosure" {
				t.Fatalf("expected disclosure header override, got %q", cfg.Prompts.Skill.DisclosureHeader)
			}
			if cfg.Prompts.Skill.InstructionsLabel != "custom label" {
				t.Fatalf("expected instructions label override, got %q", cfg.Prompts.Skill.InstructionsLabel)
			}
			if cfg.Prompts.ToolAppendix.ToolDescriptionTitle != "custom tool title" {
				t.Fatalf("expected tool description title override, got %q", cfg.Prompts.ToolAppendix.ToolDescriptionTitle)
			}
			if cfg.Prompts.ToolAppendix.AfterCallHintTitle != "custom hint title" {
				t.Fatalf("expected after call hint title override, got %q", cfg.Prompts.ToolAppendix.AfterCallHintTitle)
			}
			if cfg.Prompts.PlanExecute.TaskExecutionPromptTemplate != "custom task {{task_id}}" {
				t.Fatalf("expected task prompt override, got %q", cfg.Prompts.PlanExecute.TaskExecutionPromptTemplate)
			}
			if cfg.Prompts.PlanExecute.PlanUserPromptTemplate != "custom plan {{user_request}}" {
				t.Fatalf("expected plan user prompt override, got %q", cfg.Prompts.PlanExecute.PlanUserPromptTemplate)
			}
			if cfg.Prompts.PlanExecute.SummarySystemPrompt != "custom summary system" {
				t.Fatalf("expected summary system prompt override, got %q", cfg.Prompts.PlanExecute.SummarySystemPrompt)
			}
			if cfg.Prompts.PlanExecute.SummaryUserPromptTemplate != "custom summary {{task_results}}" {
				t.Fatalf("expected summary user prompt override, got %q", cfg.Prompts.PlanExecute.SummaryUserPromptTemplate)
			}
			if cfg.CoderPrompts.SystemPrompt != "custom coder system\nread before editing" {
				t.Fatalf("expected coder system prompt override, got %q", cfg.CoderPrompts.SystemPrompt)
			}
			if cfg.CoderPrompts.PlanningPrompt != "custom coder planning\nuse finalize_planning only" {
				t.Fatalf("expected coder planning prompt override, got %q", cfg.CoderPrompts.PlanningPrompt)
			}
			if cfg.CoderPrompts.SummarySystemPrompt != "custom coder summary system" {
				t.Fatalf("expected coder summary system prompt override, got %q", cfg.CoderPrompts.SummarySystemPrompt)
			}
			if cfg.CoderPrompts.SummaryUserPromptTemplate != "custom coder summary {{confirmed_plan}}" {
				t.Fatalf("expected coder summary user prompt override, got %q", cfg.CoderPrompts.SummaryUserPromptTemplate)
			}
			if cfg.MemoryPrompts.SystemPromptTemplate != "custom memory system\n{{task_instruction}}" {
				t.Fatalf("expected memory system prompt override, got %q", cfg.MemoryPrompts.SystemPromptTemplate)
			}
			if cfg.MemoryPrompts.UserPromptTemplate != "custom memory user\n{{source_text}}" {
				t.Fatalf("expected memory user prompt override, got %q", cfg.MemoryPrompts.UserPromptTemplate)
			}
		})
	})
}

func TestLoadCoderPromptsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"coder:\n" +
			"  system-prompt: |\n" +
			"    custom coder system\n" +
			"    read before editing\n" +
			"  planning-prompt: |\n" +
			"    custom coder planning\n" +
			"    use finalize_planning only\n" +
			"  summary-system-prompt: custom coder summary system\n" +
			"  summary-user-prompt-template: |\n" +
			"    custom coder summary {{confirmed_plan}}\n"
		withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.CoderPrompts.SystemPrompt != "custom coder system\nread before editing" {
				t.Fatalf("expected coder system prompt override, got %q", cfg.CoderPrompts.SystemPrompt)
			}
			want := "custom coder planning\nuse finalize_planning only"
			if cfg.CoderPrompts.PlanningPrompt != want {
				t.Fatalf("expected coder planning prompt %q, got %q", want, cfg.CoderPrompts.PlanningPrompt)
			}
			if cfg.CoderPrompts.SummarySystemPrompt != "custom coder summary system" {
				t.Fatalf("expected coder summary system prompt override, got %q", cfg.CoderPrompts.SummarySystemPrompt)
			}
			if cfg.CoderPrompts.SummaryUserPromptTemplate != "custom coder summary {{confirmed_plan}}" {
				t.Fatalf("expected coder summary user prompt override, got %q", cfg.CoderPrompts.SummaryUserPromptTemplate)
			}
		})
	})
}

func TestLoadMemoryPromptsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"memory:\n" +
			"  system-prompt-template: |\n" +
			"    custom memory system\n" +
			"    {{task_instruction}}\n" +
			"  user-prompt-template: |\n" +
			"    custom memory user\n" +
			"    {{source_text}}\n"
		withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.MemoryPrompts.SystemPromptTemplate != "custom memory system\n{{task_instruction}}" {
				t.Fatalf("expected memory system prompt override, got %q", cfg.MemoryPrompts.SystemPromptTemplate)
			}
			if cfg.MemoryPrompts.UserPromptTemplate != "custom memory user\n{{source_text}}" {
				t.Fatalf("expected memory user prompt override, got %q", cfg.MemoryPrompts.UserPromptTemplate)
			}
		})
	})
}

func TestLoadCoderSettingsMissingFileLeavesEmpty(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "coder-settings.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.CoderSettings.WorkspaceAgents.Enabled || cfg.CoderSettings.WorkspaceAgents.File != "" {
				t.Fatalf("expected empty coder workspace agents config, got %#v", cfg.CoderSettings.WorkspaceAgents)
			}
			if cfg.CoderSettings.DefaultAgent.ModelKey != "" || cfg.CoderSettings.DefaultAgent.ReasoningEffort != "" {
				t.Fatalf("expected empty coder default agent config, got %#v", cfg.CoderSettings.DefaultAgent)
			}
			if len(cfg.CoderSettings.ACPProxies) != 0 {
				t.Fatalf("expected empty coder ACP proxies config, got %#v", cfg.CoderSettings.ACPProxies)
			}
		})
	})
}

func TestLoadCoderSettingsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, map[string]string{"CODEX_ACP_PROXY_TOKEN": "coder-token"}, func() {
		content := "" +
			"default-agent:\n" +
			"  modelKey: deepseek-v4-pro\n" +
			"  reasoningEffort: MEDIUM\n" +
			"acp-proxies:\n" +
			"  codex:\n" +
			"    base-url: http://127.0.0.1:3211\n" +
			"    auth-token: ${CODEX_ACP_PROXY_TOKEN:}\n" +
			"  codex-alt:\n" +
			"    base-url: http://127.0.0.1:3212\n" +
			"    timeout: 420\n" +
			"workspace-agents:\n" +
			"  enabled: true\n" +
			"  file: RULES.md\n"
		withProjectFileContents(t, filepath.Join("configs", "coder-settings.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !cfg.CoderSettings.WorkspaceAgents.Enabled || cfg.CoderSettings.WorkspaceAgents.File != "RULES.md" {
				t.Fatalf("unexpected coder workspace agents override: %#v", cfg.CoderSettings.WorkspaceAgents)
			}
			if cfg.CoderSettings.DefaultAgent.ModelKey != "deepseek-v4-pro" || cfg.CoderSettings.DefaultAgent.ReasoningEffort != "MEDIUM" {
				t.Fatalf("unexpected coder default agent override: %#v", cfg.CoderSettings.DefaultAgent)
			}
			if got := cfg.CoderSettings.ACPProxies["codex"]; got.BaseURL != "http://127.0.0.1:3211" || got.AuthToken != "coder-token" || got.Timeout != 300 {
				t.Fatalf("unexpected codex ACP proxy config: %#v", got)
			}
			if got := cfg.CoderSettings.ACPProxies["codex-alt"]; got.BaseURL != "http://127.0.0.1:3212" || got.Timeout != 420 {
				t.Fatalf("unexpected codex-alt ACP proxy config: %#v", got)
			}
		})
	})
}

func TestLoadCoderSettingsRejectsACPProxyWithoutBaseURL(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"acp-proxies:\n" +
			"  codex:\n" +
			"    timeout: 300\n"
		withProjectFileContents(t, filepath.Join("configs", "coder-settings.yml"), &content, func() {
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "acp-proxies.codex.base-url is required") {
				t.Fatalf("expected missing base-url error, got %v", err)
			}
		})
	})
}

func TestLoadVisionRecognizeMissingFileDefaultsDisabled(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), nil, func() {
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
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), &content, func() {
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
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), nil, func() {
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
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), &content, func() {
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
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), nil, func() {
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
			"      model-key: babelark-gemini-3_1-flash-image-preview\n" +
			"      endpoint-path: /v1/images/generations\n"
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), &content, func() {
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
			if profile.ModelKey != "babelark-gemini-3_1-flash-image-preview" ||
				profile.Timeout != 120 ||
				profile.Size != "1024x1024" ||
				profile.ResponseFormat != "b64_json" ||
				profile.OutputMimeType != "image/png" ||
				profile.MaxPromptChars != 4000 ||
				!profile.PersistArtifact ||
				profile.EndpointPath != "/v1/images/generations" {
				t.Fatalf("unexpected profile defaults: %#v", profile)
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
			"  profiles: {}\n" +
			"speech:\n" +
			"  speech-to-text:\n" +
			"    enabled: false\n" +
			"    profiles: {}\n" +
			"  text-to-speech:\n" +
			"    enabled: false\n" +
			"    profiles: {}\n"
		withProjectFileContents(t, filepath.Join("configs", "vision-recognize.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), &content, func() {
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
	withIsolatedEnv(t, map[string]string{
		"AP_AUTH_LOCAL_PUBLIC_KEY_FILE": "ap-auth.pem",
		"AUTH_LOCAL_PUBLIC_KEY_FILE":    "legacy-auth.pem",
	}, func() {
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

func TestLoadIgnoresAPPrefixedAuthEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_AUTH_ENABLED":            "false",
		"AP_AUTH_JWKS_URI":           "https://ap.example/jwks.json",
		"AP_AUTH_ISSUER":             "ap-issuer",
		"AP_AUTH_JWKS_CACHE_SECONDS": "46",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Auth.Enabled ||
			cfg.Auth.JWKSURI != "" ||
			cfg.Auth.Issuer != "" ||
			cfg.Auth.JWKSCacheSeconds != 0 {
			t.Fatalf("expected AP auth env to be ignored, got %#v", cfg.Auth)
		}
	})
}

func TestLoadIgnoresLegacyAuthEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AUTH_ENABLED":            "false",
		"AUTH_JWKS_URI":           "https://legacy.example/jwks.json",
		"AUTH_ISSUER":             "legacy-issuer",
		"AUTH_JWKS_CACHE_SECONDS": "47",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Auth.Enabled ||
			cfg.Auth.JWKSURI != "" ||
			cfg.Auth.Issuer != "" ||
			cfg.Auth.JWKSCacheSeconds != 0 {
			t.Fatalf("expected legacy auth env to be ignored, got %#v", cfg.Auth)
		}
	})
}

func TestLoadRuntimeYAMLAuthOverridesIgnoredEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_AUTH_ENABLED":            "true",
		"AP_AUTH_JWKS_URI":           "https://ap.example/jwks.json",
		"AP_AUTH_ISSUER":             "ap-issuer",
		"AP_AUTH_JWKS_CACHE_SECONDS": "46",
		"AUTH_ENABLED":               "false",
		"AUTH_JWKS_URI":              "https://legacy.example/jwks.json",
		"AUTH_ISSUER":                "legacy-issuer",
		"AUTH_JWKS_CACHE_SECONDS":    "47",
	}, func() {
		content := "" +
			"auth:\n" +
			"  enabled: false\n" +
			"  jwks-uri: https://runtime.example/jwks.json\n" +
			"  issuer: runtime-issuer\n" +
			"  jwks-cache-seconds: 48\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Auth.Enabled ||
				cfg.Auth.JWKSURI != "https://runtime.example/jwks.json" ||
				cfg.Auth.Issuer != "runtime-issuer" ||
				cfg.Auth.JWKSCacheSeconds != 48 {
				t.Fatalf("expected runtime yaml auth to win, got %#v", cfg.Auth)
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
		filepath.Join(configsDir, "prompts.yml"),
		[]byte("skill:\n  catalog-header: service config header\ncoder:\n  system-prompt: service coder system\n  planning-prompt: service coder plan\n"),
		0o644,
	); err != nil {
		t.Fatalf("write prompts config: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "ai-tools.yml"),
		[]byte("vision-recognize:\n  enabled: true\n  default-profile: service\n"),
		0o644,
	); err != nil {
		t.Fatalf("write ai tools config: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "tools.yml"),
		[]byte("bash:\n  shell-executable: service-shell\n"),
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
		if cfg.Memory.StorageDir != filepath.Join("var", "custom-memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
		if cfg.Logging.Memory.File != filepath.Join("var", "custom-memory", "memory.log") {
			t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
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
		if cfg.Providers.ExternalDir != filepath.Join(runtimeRoot, "registries", "providers") {
			t.Fatalf("unexpected providers dir: %q", cfg.Providers.ExternalDir)
		}
		if cfg.Models.ExternalDir != filepath.Join(runtimeRoot, "registries", "models") {
			t.Fatalf("unexpected models dir: %q", cfg.Models.ExternalDir)
		}
		if cfg.Memory.StorageDir != filepath.Join(runtimeRoot, "memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
		if cfg.Logging.Memory.File != filepath.Join(runtimeRoot, "memory", "memory.log") {
			t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
		}
	})
}

func TestLoadRuntimePathsFromYAML(t *testing.T) {
	runtimeConfig := "" +
		"paths:\n" +
		"  registries-dir: var/yaml-registries\n" +
		"  tools-dir: var/yaml-tools\n" +
		"  owner-dir: var/yaml-owner\n" +
		"  agents-dir: var/yaml-agents\n" +
		"  teams-dir: var/yaml-teams\n" +
		"  root-dir: var/yaml-root\n" +
		"  automations-dir: var/yaml-automations\n" +
		"  chats-dir: var/yaml-chats\n" +
		"  memory-dir: var/yaml-memory\n" +
		"  pan-dir: var/yaml-pan\n" +
		"  skills-market-dir: var/yaml-skills-market\n"
	withIsolatedEnv(t, nil, func() {
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &runtimeConfig, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Paths.RegistriesDir != filepath.Join("var", "yaml-registries") {
				t.Fatalf("unexpected registries dir: %q", cfg.Paths.RegistriesDir)
			}
			if cfg.Paths.ToolsDir != filepath.Join("var", "yaml-tools") {
				t.Fatalf("unexpected tools dir: %q", cfg.Paths.ToolsDir)
			}
			if cfg.Paths.OwnerDir != filepath.Join("var", "yaml-owner") {
				t.Fatalf("unexpected owner dir: %q", cfg.Paths.OwnerDir)
			}
			if cfg.Paths.AgentsDir != filepath.Join("var", "yaml-agents") {
				t.Fatalf("unexpected agents dir: %q", cfg.Paths.AgentsDir)
			}
			if cfg.Paths.TeamsDir != filepath.Join("var", "yaml-teams") {
				t.Fatalf("unexpected teams dir: %q", cfg.Paths.TeamsDir)
			}
			if cfg.Paths.RootDir != filepath.Join("var", "yaml-root") {
				t.Fatalf("unexpected root dir: %q", cfg.Paths.RootDir)
			}
			if cfg.Paths.AutomationsDir != filepath.Join("var", "yaml-automations") {
				t.Fatalf("unexpected automations dir: %q", cfg.Paths.AutomationsDir)
			}
			if cfg.Paths.ChatsDir != filepath.Join("var", "yaml-chats") {
				t.Fatalf("unexpected chats dir: %q", cfg.Paths.ChatsDir)
			}
			if cfg.Paths.MemoryDir != filepath.Join("var", "yaml-memory") {
				t.Fatalf("unexpected memory dir: %q", cfg.Paths.MemoryDir)
			}
			if cfg.Paths.PanDir != filepath.Join("var", "yaml-pan") {
				t.Fatalf("unexpected pan dir: %q", cfg.Paths.PanDir)
			}
			if cfg.Paths.SkillsMarketDir != filepath.Join("var", "yaml-skills-market") {
				t.Fatalf("unexpected skills market dir: %q", cfg.Paths.SkillsMarketDir)
			}
			if cfg.Providers.ExternalDir != filepath.Join("var", "yaml-registries", "providers") {
				t.Fatalf("unexpected providers dir: %q", cfg.Providers.ExternalDir)
			}
			if cfg.Logging.LLMInteraction.RecordDir != filepath.Join("var", "yaml-chats") {
				t.Fatalf("unexpected llm chat record dir: %q", cfg.Logging.LLMInteraction.RecordDir)
			}
			if cfg.Memory.StorageDir != filepath.Join("var", "yaml-memory") {
				t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
			}
		})
	})
}

func TestLoadIgnoresRemovedRuntimeDirectoryEnvs(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		removedRuntimeDirectoryEnvKey("RUNTIME"):    filepath.Join("var", "removed-runtime"),
		removedRuntimeDirectoryEnvKey("REGISTRIES"): filepath.Join("var", "removed-registries"),
		removedRuntimeDirectoryEnvKey("CHATS"):      filepath.Join("var", "removed-chats"),
		removedRuntimeDirectoryEnvKey("MEMORY"):     filepath.Join("var", "removed-memory"),
		removedRuntimeDirectoryEnvKey("PAN"):        filepath.Join("var", "removed-pan"),
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Paths.RegistriesDir != filepath.Join("runtime", "registries") {
			t.Fatalf("unexpected registries dir: %q", cfg.Paths.RegistriesDir)
		}
		if cfg.Paths.ChatsDir != filepath.Join("runtime", "chats") {
			t.Fatalf("unexpected chats dir: %q", cfg.Paths.ChatsDir)
		}
		if cfg.Paths.MemoryDir != filepath.Join("runtime", "memory") {
			t.Fatalf("unexpected memory dir: %q", cfg.Paths.MemoryDir)
		}
		if cfg.Paths.PanDir != filepath.Join("runtime", "pan") {
			t.Fatalf("unexpected pan dir: %q", cfg.Paths.PanDir)
		}
		if cfg.Memory.StorageDir != filepath.Join("runtime", "memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
	})
}

func TestLoadIgnoresUnsupportedRuntimePathEnvs(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"OWNER_DIR":         filepath.Join("var", "env-owner"),
		"AGENTS_DIR":        filepath.Join("var", "env-agents"),
		"TEAMS_DIR":         filepath.Join("var", "env-teams"),
		"ROOT_DIR":          filepath.Join("var", "env-root"),
		"AUTOMATIONS_DIR":   filepath.Join("var", "env-automations"),
		"SKILLS_MARKET_DIR": filepath.Join("var", "env-skills-market"),
		"TOOLS_DIR":         filepath.Join("var", "env-tools"),
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Paths.OwnerDir != filepath.Join("runtime", "owner") {
			t.Fatalf("expected OWNER_DIR to be ignored, got %q", cfg.Paths.OwnerDir)
		}
		if cfg.Paths.AgentsDir != filepath.Join("runtime", "agents") {
			t.Fatalf("expected AGENTS_DIR to be ignored, got %q", cfg.Paths.AgentsDir)
		}
		if cfg.Paths.TeamsDir != filepath.Join("runtime", "teams") {
			t.Fatalf("expected TEAMS_DIR to be ignored, got %q", cfg.Paths.TeamsDir)
		}
		if cfg.Paths.RootDir != filepath.Join("runtime", "root") {
			t.Fatalf("expected ROOT_DIR to be ignored, got %q", cfg.Paths.RootDir)
		}
		if cfg.Paths.AutomationsDir != filepath.Join("runtime", "automations") {
			t.Fatalf("expected AUTOMATIONS_DIR to be ignored, got %q", cfg.Paths.AutomationsDir)
		}
		if cfg.Paths.SkillsMarketDir != filepath.Join("runtime", "skills-market") {
			t.Fatalf("expected SKILLS_MARKET_DIR to be ignored, got %q", cfg.Paths.SkillsMarketDir)
		}
		if cfg.Paths.ToolsDir != filepath.Join("runtime", "tools") {
			t.Fatalf("expected TOOLS_DIR to be ignored, got %q", cfg.Paths.ToolsDir)
		}
	})
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
		if cfg.Logging.Memory.File != filepath.Join("var", "custom-memory", "memory.log") {
			t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
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
		if cfg.Memory.StorageDir != filepath.Join("var", "custom-memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
	})
}

func TestLoadIgnoresLoggingMemoryRuntimeYAML(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_RUNTIME_MEMORY_DIR":     filepath.Join("var", "custom-memory"),
		"LOGGING_AGENT_MEMORY_FILE": filepath.Join("var", "custom-log", "memory.log"),
		"LOGGING_MEMORY_ENABLED":    "false",
	}, func() {
		content := "" +
			"logging:\n" +
			"  memory:\n" +
			"    enabled: false\n" +
			"    file: " + filepath.ToSlash(filepath.Join("var", "custom-log", "memory.log")) + "\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Logging.Memory.File != filepath.Join("var", "custom-memory", "memory.log") {
				t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
			}
			if !cfg.Logging.Memory.Enabled {
				t.Fatalf("expected memory logging to keep source default enabled")
			}
		})
	})
}

func TestLoadRuntimeYAMLReplacesLegacyEnvContract(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AUTH_ENABLED":                            "false",
		"CHAT_RESOURCE_TICKET_SECRET":             "secret",
		"AGENT_H2A_RENDER_FLUSH_INTERVAL":         "25",
		"AGENT_H2A_RENDER_MAX_BUFFERED_CHARS":     "256",
		"AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS":    "3",
		"AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH": "false",
		"AGENT_RUN_MAX_BACKGROUND_DURATION":       "601",
		"AGENT_RUN_MAX_DISCONNECTED_WAIT":         "603",
		"AGENT_WS_PING_INTERVAL":                  "32",
		"AGENT_WS_WRITE_TIMEOUT":                  "16",
		"AGENT_AUTOMATION_ENABLED":                "false",
		"AGENT_AUTOMATION_DEFAULT_ZONE_ID":        "Asia/Shanghai",
		"AGENT_AUTOMATION_POOL_SIZE":              "7",
		"LOGGING_AGENT_REQUEST_ENABLED":           "false",
	}, func() {
		content := "" +
			"auth:\n" +
			"  enabled: false\n" +
			"resource:\n" +
			"  ticket-ttl-seconds: 321\n" +
			"h2a:\n" +
			"  render:\n" +
			"    flush-interval: 25\n" +
			"    max-buffered-chars: 256\n" +
			"    max-buffered-events: 3\n" +
			"    heartbeat-pass-through: false\n" +
			"i18n:\n" +
			"  default-locale: zh-CN\n" +
			"chat-storage:\n" +
			"  k: 3\n" +
			"  charset: GBK\n" +
			"  action-tools: [legacy]\n" +
			"  index-sqlite-file: legacy.db\n" +
			"  index-auto-rebuild-on-incompatible-schema: false\n" +
			"run:\n" +
			"  max-background-duration: 601\n" +
			"  max-disconnected-wait: 603\n" +
			"websocket:\n" +
			"  ping-interval: 32\n" +
			"  write-timeout: 16\n" +
			"automation:\n" +
			"  enabled: false\n" +
			"  default-zone-id: Asia/Shanghai\n" +
			"  pool-size: 7\n" +
			"logging:\n" +
			"  request:\n" +
			"    enabled: false\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Auth.Enabled {
				t.Fatalf("expected auth disabled from runtime yaml")
			}
			if cfg.ResourceTicket.Secret != "" || cfg.ResourceTicket.TTLSeconds != 321 {
				t.Fatalf("unexpected resource ticket config: %#v", cfg.ResourceTicket)
			}
			if cfg.Run.MaxBackgroundDuration != 0 || cfg.Run.MaxDisconnectedWait != 0 {
				t.Fatalf("expected runtime yaml run lifecycle config to be ignored, got %#v", cfg.Run)
			}
			if cfg.WebSocket.PingInterval != 0 || cfg.WebSocket.WriteTimeout != 0 {
				t.Fatalf("expected runtime yaml websocket config to be ignored, got %#v", cfg.WebSocket)
			}
			if cfg.Automation.Enabled ||
				cfg.Automation.DefaultZoneID != "Asia/Shanghai" ||
				cfg.Automation.PoolSize != 7 {
				t.Fatalf("unexpected automation config: %#v", cfg.Automation)
			}
			if !cfg.Logging.Request.Enabled {
				t.Fatalf("expected runtime yaml logging request config to be ignored")
			}
		})
	})
}

func TestLoadAcceptsAPPrefixedEnvContract(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_CHAT_RESOURCE_TICKET_SECRET":          "ap-secret",
		"AP_DEBUG_LLM_CONSOLE":                    "raw,parsed",
		"AP_DEBUG_LLM_CHAT_RECORD":                "true",
		"AP_CONTAINER_HUB_BASE_URL":               "http://ap-hub",
		"AP_CONTAINER_HUB_AUTH_TOKEN":             "ap-token",
		"AP_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID": "ap-env",
		"AP_CONTAINER_HUB_REQUEST_TIMEOUT":        "302",
		"AP_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL":  "AGENT",
		"AP_CONTAINER_HUB_AGENT_IDLE_TIMEOUT":     "303",
		"AP_CONTAINER_HUB_DESTROY_QUEUE_DELAY":    "304",
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

func TestLoadAPPrefixedEnvOverridesLegacyEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_CHAT_RESOURCE_TICKET_SECRET":          "ap-secret",
		"AP_DEBUG_LLM_CONSOLE":                    "raw,parsed",
		"AP_DEBUG_LLM_CHAT_RECORD":                "true",
		"AP_CONTAINER_HUB_BASE_URL":               "http://ap-hub",
		"AP_CONTAINER_HUB_AUTH_TOKEN":             "ap-token",
		"AP_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID": "ap-env",
		"AP_CONTAINER_HUB_REQUEST_TIMEOUT":        "302",
		"AP_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL":  "AGENT",
		"AP_CONTAINER_HUB_AGENT_IDLE_TIMEOUT":     "303",
		"AP_CONTAINER_HUB_DESTROY_QUEUE_DELAY":    "304",
		"CHAT_RESOURCE_TICKET_SECRET":             "legacy-secret",
		"DEBUG_LLM_CONSOLE":                       "none",
		"DEBUG_LLM_CHAT_RECORD":                   "false",
		"CONTAINER_HUB_BASE_URL":                  "http://legacy-hub",
		"CONTAINER_HUB_AUTH_TOKEN":                "legacy-token",
		"CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID":    "legacy-env",
		"CONTAINER_HUB_REQUEST_TIMEOUT":           "402",
		"CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL":     "legacy",
		"CONTAINER_HUB_AGENT_IDLE_TIMEOUT":        "403",
		"CONTAINER_HUB_DESTROY_QUEUE_DELAY":       "404",
	}, func() {
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.ResourceTicket.Secret != "ap-secret" || cfg.ResourceTicket.TTLSeconds != 86400 {
				t.Fatalf("expected AP resource ticket secret to win without changing ttl, got %#v", cfg.ResourceTicket)
			}
			if got := strings.Join(cfg.Logging.LLMInteraction.ConsoleCategories, ","); got != "raw,parsed" {
				t.Fatalf("expected AP llm console categories to win, got %q", got)
			}
			if !cfg.Logging.LLMInteraction.RecordEnabled {
				t.Fatalf("expected AP llm chat record flag to win")
			}
			if cfg.ContainerHub.BaseURL != "http://ap-hub" ||
				cfg.ContainerHub.AuthToken != "" ||
				cfg.ContainerHub.DefaultEnvironmentID != "" ||
				cfg.ContainerHub.RequestTimeout != 300 ||
				cfg.ContainerHub.DefaultSandboxLevel != "run" ||
				cfg.ContainerHub.AgentIdleTimeout != 600 ||
				cfg.ContainerHub.DestroyQueueDelay != 5 {
				t.Fatalf("expected only AP container hub base url env to win, got %#v", cfg.ContainerHub)
			}
		})
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

func TestLoadAPEnvAndRuntimeYAMLWithToolsYAMLConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AP_CONTAINER_HUB_BASE_URL": "http://127.0.0.1:18000",
	}, func() {
		content := "" +
			"bash:\n" +
			"  working-directory: " + filepath.ToSlash(filepath.Join("var", "runtime")) + "\n" +
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
				if cfg.Bash.WorkingDirectory != filepath.Join("var", "runtime") {
					t.Fatalf("unexpected working directory: %q", cfg.Bash.WorkingDirectory)
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
					cfg.Defaults.Budget.Hitl.Form.Timeout != 76 ||
					cfg.Defaults.Budget.Hitl.Plan.Timeout != 80 {
					t.Fatalf("unexpected default HITL mode budget timeout: %#v", cfg.Defaults.Budget.Hitl)
				}
			})
		})
	})
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
			"  working-directory: \"@workspace\"\n" +
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
			if strings.Join(level.ReadRoots, ",") != "@workspace,@chat" {
				t.Fatalf("unexpected read roots: %#v", level.ReadRoots)
			}
			if strings.Join(level.WriteRoots, ",") != "@workspace,@chat" {
				t.Fatalf("unexpected write roots: %#v", level.WriteRoots)
			}
			if level.Approvals.ReadOutsideRoots != "block" {
				t.Fatalf("unexpected read outside action: %#v", level.Approvals)
			}
		})
	})
}

func TestSandboxBashConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"access-policy:\n" +
			"  levels:\n" +
			"    auto_approve:\n" +
			"      inherit: default\n" +
			"      approvals:\n" +
			"        bash-write-in-write-roots: allow\n" +
			"sandbox-bash:\n" +
			"  security:\n" +
			"    bashsec-overrides:\n" +
			"      output-redirection: auto\n" +
			"      heredoc-output-redirection: nope\n" +
			"    audit-auto-approvals: true\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.SandboxBash.Security.BashsecOverrides.OutputRedirection != "auto" {
				t.Fatalf("unexpected output redirection override: %#v", cfg.SandboxBash)
			}
			if cfg.SandboxBash.Security.BashsecOverrides.HeredocOutputRedirection != "" {
				t.Fatalf("expected invalid heredoc override to fall back to empty, got %#v", cfg.SandboxBash)
			}
			if !cfg.SandboxBash.Security.AuditAutoApprovals {
				t.Fatalf("expected sandbox bash auto approvals to be audited")
			}
			autoLevel := cfg.AccessPolicy.Levels["auto_approve"]
			if autoLevel.Approvals.BashWriteInWriteRoots != "allow" {
				t.Fatalf("expected explicit auto_approve bash write action, got %#v", autoLevel.Approvals)
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
			"  working-directory: " + filepath.ToSlash(filepath.Join("tmp", "files")) + "\n" +
			"  max-read-bytes: 1234\n" +
			"  max-write-bytes: 5678\n" +
			"  max-batch-ops: 9\n" +
			"  require-write-approval: false\n" +
			"  require-read-before-write: false\n" +
			"  read-before-write-scope: chat\n"
		withProjectFileContents(t, filepath.Join("configs", "tools.yml"), &content, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.FileTools.WorkingDirectory != filepath.Join("tmp", "files") {
				t.Fatalf("unexpected file working dir: %q", cfg.FileTools.WorkingDirectory)
			}
			if cfg.FileTools.MaxReadBytes != 1234 || cfg.FileTools.MaxWriteBytes != 5678 || cfg.FileTools.MaxBatchOps != 9 {
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
			"  working-directory: \"@workspace\"\n" +
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
			"  working-directory: " + filepath.ToSlash(filepath.Join("var", "host")) + "\n" +
			"  allowed-commands: pwd,echo\n" +
			"  shell-features-enabled: true\n" +
			"  shell-executable: bash\n" +
			"  shell-timeout: 12345\n" +
			"  max-command-chars: 4321\n" +
			"file-tools:\n" +
			"  working-directory: " + filepath.ToSlash(filepath.Join("tmp", "merged-files")) + "\n" +
			"  max-read-bytes: 1234\n" +
			"  max-write-bytes: 5678\n" +
			"  max-batch-ops: 9\n" +
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
						if strings.Join(level.ReadRoots, ",") != "@workspace" {
							t.Fatalf("unexpected read roots: %#v", level.ReadRoots)
						}
						if level.Approvals.ReadOutsideRoots != "block" {
							t.Fatalf("unexpected read outside action: %#v", level.Approvals)
						}
						if cfg.Bash.WorkingDirectory != filepath.Join("var", "host") || cfg.Bash.ShellExecutable != "bash" || cfg.Bash.ShellTimeout != 12345 || cfg.Bash.MaxCommandChars != 4321 {
							t.Fatalf("unexpected bash config: %#v", cfg.Bash)
						}
						if strings.Join(cfg.Bash.AllowedCommands, ",") != "pwd,echo" {
							t.Fatalf("unexpected allowed commands: %#v", cfg.Bash.AllowedCommands)
						}
						if cfg.FileTools.WorkingDirectory != filepath.Join("tmp", "merged-files") {
							t.Fatalf("unexpected file working dir: %q", cfg.FileTools.WorkingDirectory)
						}
						if cfg.FileTools.MaxReadBytes != 1234 || cfg.FileTools.MaxWriteBytes != 5678 || cfg.FileTools.MaxBatchOps != 9 {
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

func TestLoadIgnoresLegacyLLMDebugEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"DEBUG_LLM_CONSOLE":     "raw,parsed",
		"DEBUG_LLM_CHAT_RECORD": "true",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if got := strings.Join(cfg.Logging.LLMInteraction.ConsoleCategories, ","); got != "request,usage" {
			t.Fatalf("expected legacy llm console env to be ignored, got %q", got)
		}
		if cfg.Logging.LLMInteraction.RecordEnabled {
			t.Fatalf("expected legacy llm chat record env to be ignored")
		}
	})
}

func TestLoadIgnoresOldGatewayEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"GATEWAY_WS_URL":                        "wss://gw.example.com/ws/agent?key=zenmi&channel=wecom:xiaozhai",
		"GATEWAY_JWT_TOKEN":                     "jwt-abc",
		"AGENT_GATEWAY_WS_HANDSHAKE_TIMEOUT_MS": "3210",
		"AGENT_GATEWAY_WS_RECONNECT_MIN_MS":     "45",
		"AGENT_GATEWAY_WS_RECONNECT_MAX_MS":     "6789",
	}, func() {
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), nil, func() {
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if len(cfg.Gateways) != 0 {
				t.Fatalf("old gateway env should not synthesize gateways, got %#v", cfg.Gateways)
			}
		})
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

func TestLoadChannelsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"WECOM_BRIDGE_WS_URL":    "wss://bridge.example.com/ws/agent?channel=wecom:corp1",
		"WECOM_BRIDGE_JWT_TOKEN": "jwt-wecom",
	}, func() {
		content := "" +
			"channels:\n" +
			"  wecom:\n" +
			"    name: 企业微信\n" +
			"    type: bridge\n" +
			"    default-agent: customer-service\n" +
			"    agents: \"*\"\n" +
			"    gateway:\n" +
			"      url: ${WECOM_BRIDGE_WS_URL}\n" +
			"      jwt-token: ${WECOM_BRIDGE_JWT_TOKEN}\n" +
			"  feishu:\n" +
			"    name: 飞书\n" +
			"    type: gateway\n" +
			"    agents:\n" +
			"      - assistant\n" +
			"      - code-helper\n" +
			"    gateway:\n" +
			"      url: ws://gateway.example.com/ws/agent?channel=feishu\n" +
			"      base-url: ${FEISHU_BASE_URL:http://gateway.example.com}\n"
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
			if !byID["wecom"].AllAgents || byID["wecom"].DefaultAgent != "customer-service" {
				t.Fatalf("unexpected wecom channel: %#v", byID["wecom"])
			}
			if byID["wecom"].Gateway.URL != "wss://bridge.example.com/ws/agent?channel=wecom:corp1" {
				t.Fatalf("unexpected wecom gateway url: %q", byID["wecom"].Gateway.URL)
			}
			if byID["wecom"].Gateway.JwtToken != "jwt-wecom" {
				t.Fatalf("unexpected wecom gateway token: %q", byID["wecom"].Gateway.JwtToken)
			}
			if byID["feishu"].AllAgents {
				t.Fatalf("expected feishu to use whitelist: %#v", byID["feishu"])
			}
			if len(byID["feishu"].Agents) != 2 || byID["feishu"].Agents[0] != "assistant" || byID["feishu"].Agents[1] != "code-helper" {
				t.Fatalf("unexpected feishu agents: %#v", byID["feishu"].Agents)
			}
			if len(cfg.Gateways) != 2 {
				t.Fatalf("expected 2 synthesized gateways, got %d", len(cfg.Gateways))
			}
			gatewaysByID := map[string]GatewayEntry{}
			for _, gateway := range cfg.Gateways {
				gatewaysByID[gateway.ID] = gateway
			}
			if gatewaysByID["wecom"].Channel != "wecom" {
				t.Fatalf("unexpected synthesized wecom channel: %#v", gatewaysByID["wecom"])
			}
			if gatewaysByID["wecom"].SourceChannel != "wecom:corp1" || gatewaysByID["wecom"].SourcePrefix != "wecom" {
				t.Fatalf("unexpected synthesized wecom source route: %#v", gatewaysByID["wecom"])
			}
			if gatewaysByID["feishu"].BaseURL != "http://gateway.example.com" {
				t.Fatalf("expected feishu baseURL from fallback interpolation, got %q", gatewaysByID["feishu"].BaseURL)
			}
		})
	})
}

func TestLoadChannelsConfigAllowsCustomChannelIDForWecomSource(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  company-gateway:\n" +
			"    name: 公司网关\n" +
			"    type: bridge\n" +
			"    agents: \"*\"\n" +
			"    gateway:\n" +
			"      url: ws://zwy.zenmind.cc/ws/agent?agentKey=zenmi&channel=wecom:langyage\n" +
			"      jwt-token: token\n"
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
			"    type: invalid\n" +
			"    gateway:\n" +
			"      url: ws://gateway.example.com/ws/agent?channel=wecom\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			if _, err := Load(); err == nil {
				t.Fatalf("expected invalid channel type to fail")
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
			ID:   "wecom",
			Type: ChannelTypeBridge,
			Gateway: ChannelGatewayConfig{
				URL: "ws://bridge.example.com/ws/agent?channel=wecom:corp1",
			},
		},
	}
	if err := cfg.normalize(""); err == nil {
		t.Fatalf("expected duplicate channel gateway conflict to fail")
	}
}

func TestLoadChannelsConfigRejectsMissingGatewayURL(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  mobile:\n" +
			"    type: gateway\n" +
			"    gateway:\n" +
			"      jwt-token: token\n"
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), &content, func() {
			if _, err := Load(); err == nil {
				t.Fatalf("expected missing gateway url to fail")
			}
		})
	})
}

func TestLoadGatewayConfigFromChannels(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"channels:\n" +
			"  mobile:\n" +
			"    type: gateway\n" +
			"    agents: \"*\"\n" +
			"    gateway:\n" +
			"      url: ws://127.0.0.1:17999/gw?channel=mobile\n" +
			"      jwt-token: jwt-abc\n"
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
		"SKILLS_MARKET_DIR",
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

func removedRuntimeDirectoryEnvKey(name string) string {
	return name + "_DIR"
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
