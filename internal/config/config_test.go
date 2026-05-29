package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Server.Port != "11949" {
			t.Fatalf("expected default port 11949, got %q", cfg.Server.Port)
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
		if cfg.Billing.Currency != "CNY" {
			t.Fatalf("expected default billing currency CNY, got %q", cfg.Billing.Currency)
		}
		if !cfg.Stream.IncludeToolPayloadEvents {
			t.Fatalf("expected stream tool payload events enabled by default")
		}
		if cfg.Stream.DebugEventsEnabled {
			t.Fatalf("expected stream debug events disabled by default")
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
		if cfg.Logging.LLMInteraction.RecordEnabled {
			t.Fatalf("expected llm chat record disabled by default")
		}
		if cfg.Logging.LLMInteraction.RecordDir != filepath.Join("runtime", "chats", "llm") {
			t.Fatalf("unexpected llm chat record dir: %q", cfg.Logging.LLMInteraction.RecordDir)
		}
		if cfg.BashHITL.DefaultTimeoutMs != 120000 {
			t.Fatalf("expected default bash HITL timeout 120000, got %d", cfg.BashHITL.DefaultTimeoutMs)
		}
		if cfg.Defaults.Budget.Hitl.TimeoutMs != 0 {
			t.Fatalf("expected default HITL budget timeout 0, got %d", cfg.Defaults.Budget.Hitl.TimeoutMs)
		}
		if cfg.Defaults.Budget.Model.MaxCalls != 100 {
			t.Fatalf("expected default model max calls 100, got %d", cfg.Defaults.Budget.Model.MaxCalls)
		}
		if cfg.Defaults.Budget.MaxSteps != 100 {
			t.Fatalf("expected default budget max steps 100, got %d", cfg.Defaults.Budget.MaxSteps)
		}
		if cfg.Defaults.Budget.Tool.MaxCalls != 60 {
			t.Fatalf("expected default tool max calls 60, got %d", cfg.Defaults.Budget.Tool.MaxCalls)
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
	})
}

func TestLoadEnvBudgetMaxStepsDerivesToolMaxCalls(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AGENT_DEFAULT_BUDGET_MAX_STEPS": "17",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Defaults.Budget.MaxSteps != 17 {
			t.Fatalf("expected env max steps 17, got %d", cfg.Defaults.Budget.MaxSteps)
		}
		if cfg.Defaults.Budget.Tool.MaxCalls != 34 {
			t.Fatalf("expected derived tool max calls 34, got %d", cfg.Defaults.Budget.Tool.MaxCalls)
		}
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
			"action:\n" +
			"  host: 127.0.0.2\n" +
			"  port: 17001\n" +
			"  path: actions/custom\n" +
			"  request-timeout-ms: 1234\n" +
			"cdp:\n" +
			"  host: localhost\n" +
			"  port: 17002\n" +
			"  path: /cdp/custom\n" +
			"  request-timeout-ms: 5678\n"
		withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "desktop.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.Desktop.Action.BridgeURL != "http://127.0.0.2:17001/actions/custom" {
					t.Fatalf("unexpected desktop action bridge url: %q", cfg.Desktop.Action.BridgeURL)
				}
				if cfg.Desktop.Action.RequestTimeoutMs != 1234 {
					t.Fatalf("unexpected desktop action timeout: %d", cfg.Desktop.Action.RequestTimeoutMs)
				}
				if cfg.Desktop.CDP.BridgeURL != "http://localhost:17002/cdp/custom" {
					t.Fatalf("unexpected desktop cdp bridge url: %q", cfg.Desktop.CDP.BridgeURL)
				}
				if cfg.Desktop.CDP.RequestTimeoutMs != 5678 {
					t.Fatalf("unexpected desktop cdp timeout: %d", cfg.Desktop.CDP.RequestTimeoutMs)
				}
			})
		})
	})
}

func TestLoadRuntimeConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n" +
			"  auth-token: runtime-token\n" +
			"  default-environment-id: runtime-env\n" +
			"  request-timeout-ms: 123456\n" +
			"  default-sandbox-level: agent\n" +
			"  agent-idle-timeout-ms: 654321\n" +
			"  destroy-queue-delay-ms: 2345\n" +
			"desktop:\n" +
			"  action:\n" +
			"    host: 127.0.0.3\n" +
			"    port: 17101\n" +
			"    path: actions/runtime\n" +
			"    request-timeout-ms: 2345\n" +
			"  cdp:\n" +
			"    host: localhost\n" +
			"    port: 17102\n" +
			"    path: /cdp/runtime\n" +
			"    request-timeout-ms: 6789\n" +
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
						if cfg.ContainerHub.RequestTimeoutMs != 123456 || cfg.ContainerHub.DefaultSandboxLevel != "agent" || cfg.ContainerHub.AgentIdleTimeoutMs != 654321 || cfg.ContainerHub.DestroyQueueDelayMs != 2345 {
							t.Fatalf("unexpected container hub runtime settings: %#v", cfg.ContainerHub)
						}
						if cfg.Desktop.Action.BridgeURL != "http://127.0.0.3:17101/actions/runtime" || cfg.Desktop.Action.RequestTimeoutMs != 2345 {
							t.Fatalf("unexpected desktop action config: %#v", cfg.Desktop.Action)
						}
						if cfg.Desktop.CDP.BridgeURL != "http://localhost:17102/cdp/runtime" || cfg.Desktop.CDP.RequestTimeoutMs != 6789 {
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
					})
				})
			})
		})
	})
}

func TestLoadRuntimeConfigOverridesLegacyRuntimeFiles(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		legacyContainer := "" +
			"base-url: http://legacy-hub\n" +
			"auth-token: legacy-token\n" +
			"default-environment-id: legacy-env\n"
		legacyDesktop := "" +
			"action:\n" +
			"  host: 127.0.0.4\n" +
			"  port: 17201\n" +
			"  path: /actions/legacy\n"
		legacyCORS := "" +
			"enabled: false\n" +
			"path-pattern: /legacy/**\n"
		merged := "" +
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n" +
			"desktop:\n" +
			"  action:\n" +
			"    port: 17301\n" +
			"cors:\n" +
			"  enabled: true\n"
		withProjectFileContents(t, filepath.Join("configs", "container-hub.yml"), &legacyContainer, func() {
			withProjectFileContents(t, filepath.Join("configs", "desktop.yml"), &legacyDesktop, func() {
				withProjectFileContents(t, filepath.Join("configs", "cors.yml"), &legacyCORS, func() {
					withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &merged, func() {
						cfg, err := Load()
						if err != nil {
							t.Fatalf("load config: %v", err)
						}
						if cfg.ContainerHub.BaseURL != "http://runtime-hub" {
							t.Fatalf("expected runtime container hub base url to win, got %q", cfg.ContainerHub.BaseURL)
						}
						if cfg.ContainerHub.AuthToken != "legacy-token" || cfg.ContainerHub.DefaultEnvironmentID != "legacy-env" {
							t.Fatalf("expected legacy container hub fallback to remain, got %#v", cfg.ContainerHub)
						}
						if cfg.Desktop.Action.BridgeURL != "http://127.0.0.4:17301/actions/legacy" {
							t.Fatalf("expected runtime desktop port with legacy fallback, got %#v", cfg.Desktop.Action)
						}
						if !cfg.CORS.Enabled || cfg.CORS.PathPattern != "/legacy/**" {
							t.Fatalf("expected runtime cors enabled with legacy path fallback, got %#v", cfg.CORS)
						}
					})
				})
			})
		})
	})
}

func TestLoadEnvOverridesRuntimeYAMLConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"CONTAINER_HUB_BASE_URL": "http://env-hub",
	}, func() {
		content := "" +
			"container-hub:\n" +
			"  base-url: http://runtime-hub\n" +
			"  request-timeout-ms: 111\n"
		withProjectFileContents(t, filepath.Join("configs", "container-hub.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.ContainerHub.BaseURL != "http://env-hub" {
					t.Fatalf("expected env container hub base url to win, got %q", cfg.ContainerHub.BaseURL)
				}
				if cfg.ContainerHub.RequestTimeoutMs != 111 {
					t.Fatalf("expected runtime yaml timeout to remain, got %d", cfg.ContainerHub.RequestTimeoutMs)
				}
			})
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
			"    use planning_write only\n" +
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
			if cfg.CoderPrompts.PlanningPrompt != "custom coder planning\nuse planning_write only" {
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
			"system-prompt: |\n" +
			"  custom coder system\n" +
			"  read before editing\n" +
			"planning-prompt: |\n" +
			"  custom coder planning\n" +
			"  use planning_write only\n" +
			"summary-system-prompt: custom coder summary system\n" +
			"summary-user-prompt-template: |\n" +
			"  custom coder summary {{confirmed_plan}}\n"
		withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "coder-prompts.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if cfg.CoderPrompts.SystemPrompt != "custom coder system\nread before editing" {
					t.Fatalf("expected coder system prompt override, got %q", cfg.CoderPrompts.SystemPrompt)
				}
				want := "custom coder planning\nuse planning_write only"
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
	})
}

func TestLoadMemoryPromptsConfigFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"system-prompt-template: |\n" +
			"  custom memory system\n" +
			"  {{task_instruction}}\n" +
			"user-prompt-template: |\n" +
			"  custom memory user\n" +
			"  {{source_text}}\n"
		withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "memory-prompts.yml"), &content, func() {
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
	})
}

func TestLoadPromptsConfigOverridesLegacyPromptFiles(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		legacyCoder := "" +
			"system-prompt: legacy coder system\n" +
			"planning-prompt: legacy coder plan\n" +
			"summary-system-prompt: legacy coder summary\n"
		legacyMemory := "" +
			"system-prompt-template: legacy memory system\n" +
			"user-prompt-template: legacy memory user\n"
		merged := "" +
			"coder:\n" +
			"  system-prompt: merged coder system\n" +
			"  planning-prompt: merged coder plan\n" +
			"memory:\n" +
			"  user-prompt-template: merged memory user\n"
		withProjectFileContents(t, filepath.Join("configs", "coder-prompts.yml"), &legacyCoder, func() {
			withProjectFileContents(t, filepath.Join("configs", "memory-prompts.yml"), &legacyMemory, func() {
				withProjectFileContents(t, filepath.Join("configs", "prompts.yml"), &merged, func() {
					cfg, err := Load()
					if err != nil {
						t.Fatalf("load config: %v", err)
					}
					if cfg.CoderPrompts.PlanningPrompt != "merged coder plan" {
						t.Fatalf("expected merged coder prompt to win, got %q", cfg.CoderPrompts.PlanningPrompt)
					}
					if cfg.CoderPrompts.SystemPrompt != "merged coder system" {
						t.Fatalf("expected merged coder system prompt to win, got %q", cfg.CoderPrompts.SystemPrompt)
					}
					if cfg.CoderPrompts.SummarySystemPrompt != "legacy coder summary" {
						t.Fatalf("expected legacy coder fallback to remain, got %q", cfg.CoderPrompts.SummarySystemPrompt)
					}
					if cfg.MemoryPrompts.SystemPromptTemplate != "legacy memory system" {
						t.Fatalf("expected legacy memory fallback to remain, got %q", cfg.MemoryPrompts.SystemPromptTemplate)
					}
					if cfg.MemoryPrompts.UserPromptTemplate != "merged memory user" {
						t.Fatalf("expected merged memory prompt to win, got %q", cfg.MemoryPrompts.UserPromptTemplate)
					}
				})
			})
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
			"    timeout-ms: 420000\n" +
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
			if got := cfg.CoderSettings.ACPProxies["codex"]; got.BaseURL != "http://127.0.0.1:3211" || got.AuthToken != "coder-token" || got.TimeoutMs != 300000 {
				t.Fatalf("unexpected codex ACP proxy config: %#v", got)
			}
			if got := cfg.CoderSettings.ACPProxies["codex-alt"]; got.BaseURL != "http://127.0.0.1:3212" || got.TimeoutMs != 420000 {
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
			"    timeout-ms: 300000\n"
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
			"enabled: true\n" +
			"default-profile: ocr\n" +
			"profiles:\n" +
			"  ocr:\n" +
			"    model-key: bailian-qwen3_5-plus\n" +
			"    timeout-ms: 12345\n" +
			"    max-images: 3\n" +
			"    max-image-bytes: 456789\n" +
			"    output-format: json\n" +
			"    system-prompt: |\n" +
			"      extract text\n" +
			"      return json\n"
		withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "vision-recognize.yml"), &content, func() {
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
				if profile.ModelKey != "bailian-qwen3_5-plus" || profile.TimeoutMs != 12345 || profile.MaxImages != 3 || profile.MaxImageBytes != 456789 || profile.OutputFormat != "json" {
					t.Fatalf("unexpected profile: %#v", profile)
				}
				if profile.SystemPrompt != "extract text\nreturn json" {
					t.Fatalf("unexpected system prompt: %q", profile.SystemPrompt)
				}
			})
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
			"      timeout-ms: 23456\n" +
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
				if profile.ModelKey != "bailian-qwen3_5-plus" || profile.TimeoutMs != 23456 || profile.MaxImages != 2 || profile.MaxImageBytes != 567890 || profile.OutputFormat != "json" {
					t.Fatalf("unexpected profile: %#v", profile)
				}
				if profile.SystemPrompt != "extract merged text" {
					t.Fatalf("unexpected system prompt: %q", profile.SystemPrompt)
				}
			})
		})
	})
}

func TestLoadAIToolsConfigOverridesLegacyVisionRecognizeFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		legacy := "" +
			"enabled: true\n" +
			"default-profile: legacy\n" +
			"profiles:\n" +
			"  legacy:\n" +
			"    model-key: legacy-model\n"
		merged := "" +
			"vision-recognize:\n" +
			"  default-profile: merged\n" +
			"  profiles:\n" +
			"    merged:\n" +
			"      model-key: merged-model\n"
		withProjectFileContents(t, filepath.Join("configs", "vision-recognize.yml"), &legacy, func() {
			withProjectFileContents(t, filepath.Join("configs", "ai-tools.yml"), &merged, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				if !cfg.VisionRecognize.Enabled {
					t.Fatal("expected legacy enabled flag to remain")
				}
				if cfg.VisionRecognize.DefaultProfile != "merged" {
					t.Fatalf("expected ai-tools default profile to win, got %q", cfg.VisionRecognize.DefaultProfile)
				}
				if _, ok := cfg.VisionRecognize.Profiles["legacy"]; ok {
					t.Fatalf("expected merged profiles to replace legacy profiles, got %#v", cfg.VisionRecognize.Profiles)
				}
				if cfg.VisionRecognize.Profiles["merged"].ModelKey != "merged-model" {
					t.Fatalf("expected merged model profile, got %#v", cfg.VisionRecognize.Profiles)
				}
			})
		})
	})
}

func TestLoadAuthLocalPublicKeyPathUnderConfigs(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AUTH_LOCAL_PUBLIC_KEY_FILE": "local-public-key.pem",
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
		"AUTH_LOCAL_PUBLIC_KEY_FILE": filepath.Join("configs", "custom.pem"),
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
		"AUTH_LOCAL_PUBLIC_KEY_FILE": filepath.Join(string(os.PathSeparator), "tmp", "custom.pem"),
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

func TestLoadUsesServiceConfigDirForStructuredFilesAndAuthKey(t *testing.T) {
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
		filepath.Join(configsDir, "host-tools.yml"),
		[]byte("bash:\n  shell-executable: service-shell\n"),
		0o644,
	); err != nil {
		t.Fatalf("write host tools config: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configsDir, "runtime.yml"),
		[]byte("container-hub:\n  base-url: http://service-hub\ncors:\n  enabled: true\n"),
		0o644,
	); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}

	withIsolatedEnv(t, map[string]string{
		"SERVICE_CONFIG_DIR": configDir,
	}, func() {
		cfg, err := Load()
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
			t.Fatalf("expected host tools from service config dir, got %q", cfg.Bash.ShellExecutable)
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
		if cfg.Logging.Memory.File != filepath.Join("var", "custom-memory", "memory.log") {
			t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
		}
	})
}

func TestLoadRuntimeDirDerivesRuntimePaths(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"RUNTIME_DIR":      filepath.Join("var", "runtime"),
		"SERVICE_DATA_DIR": filepath.Join("var", "service-data"),
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
		if cfg.ChatStorage.Dir != filepath.Join(runtimeRoot, "chats") {
			t.Fatalf("unexpected chat storage dir: %q", cfg.ChatStorage.Dir)
		}
		if cfg.Memory.StorageDir != filepath.Join(runtimeRoot, "memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
		if cfg.Logging.Memory.File != filepath.Join(runtimeRoot, "memory", "memory.log") {
			t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
		}
	})
}

func TestLoadRuntimeDirAllowsCommonDirectoryOverrides(t *testing.T) {
	panDir := filepath.Join(t.TempDir(), "custom-pan")
	if err := os.Mkdir(panDir, 0o755); err != nil {
		t.Fatalf("make pan dir: %v", err)
	}
	withIsolatedEnv(t, map[string]string{
		"RUNTIME_DIR":    filepath.Join("var", "runtime"),
		"REGISTRIES_DIR": filepath.Join("var", "custom-registries"),
		"CHATS_DIR":      filepath.Join("var", "custom-chats"),
		"MEMORY_DIR":     filepath.Join("var", "custom-memory"),
		"PAN_DIR":        panDir,
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
		if cfg.ChatStorage.Dir != filepath.Join("var", "custom-chats") {
			t.Fatalf("unexpected chat storage dir: %q", cfg.ChatStorage.Dir)
		}
		if cfg.Memory.StorageDir != filepath.Join("var", "custom-memory") {
			t.Fatalf("unexpected memory storage dir: %q", cfg.Memory.StorageDir)
		}
	})
}

func TestLoadMemoryLogFileEnvOverridesMemoryDirDefault(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"MEMORY_DIR":                filepath.Join("var", "custom-memory"),
		"LOGGING_AGENT_MEMORY_FILE": filepath.Join("var", "custom-log", "memory.log"),
		"LOGGING_MEMORY_ENABLED":    "false",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Logging.Memory.File != filepath.Join("var", "custom-log", "memory.log") {
			t.Fatalf("unexpected memory log file: %q", cfg.Logging.Memory.File)
		}
		if cfg.Logging.Memory.Enabled {
			t.Fatalf("expected memory logging to be disabled")
		}
	})
}

func TestLoadIgnoresOldEnvVars(t *testing.T) {
	values := map[string]string{
		"AGENT_CONTAINER_HUB_BASE_URL":           "http://127.0.0.1:18000",
		"AGENT_STREAM_INCLUDE_DEBUG_EVENTS":      "true",
		"AGENT_MEMORY_STORAGE_DIR":               filepath.Join("var", "custom-memory"),
		"AGENT_CONFIG_DIR":                       "configs",
		"GATEWAY_WS_URL":                         "wss://gw.example.com/ws/agent?channel=wecom",
		"AGENT_GATEWAY_WS_RECONNECT_MAX_MS":      "6789",
		"MEMORY_CHATS_INDEX_SQLITE_FILE":         "old.db",
		"AGENT_CONTAINER_HUB_REQUEST_TIMEOUT_MS": "1000",
		"AGENT_AUTH_ENABLED":                     "false",
	}
	bashAndFileEnv := []string{
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
	}
	for _, key := range bashAndFileEnv {
		values[key] = "deprecated"
	}
	withIsolatedEnv(t, values, func() {
		withProjectFileContents(t, filepath.Join("configs", "channels.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "runtime.yml"), nil, func() {
				withProjectFileContents(t, filepath.Join("configs", "container-hub.yml"), nil, func() {
					cfg, err := Load()
					if err != nil {
						t.Fatalf("load config: %v", err)
					}
					if len(cfg.Gateways) != 0 {
						t.Fatalf("old gateway env should not synthesize gateways, got %#v", cfg.Gateways)
					}
					if !cfg.Auth.Enabled {
						t.Fatalf("old auth env should not disable auth")
					}
					if cfg.ContainerHub.BaseURL != "" || cfg.ContainerHub.Enabled {
						t.Fatalf("old container hub env should not configure container hub: %#v", cfg.ContainerHub)
					}
					if cfg.Paths.MemoryDir == filepath.Join("var", "custom-memory") {
						t.Fatalf("old memory storage env should not affect memory dir")
					}
				})
			})
		})
	})
}

func TestLoadAcceptsJavaEnvContract(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"AUTH_ENABLED":                            "false",
		"CHAT_RESOURCE_TICKET_SECRET":             "secret",
		"CHAT_RESOURCE_TICKET_TTL_SECONDS":        "300",
		"STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS":      "true",
		"DEBUG_EVENTS_ENABLED":                    "true",
		"AGENT_SSE_HEARTBEAT_INTERVAL_MS":         "3000",
		"AGENT_H2A_RENDER_FLUSH_INTERVAL_MS":      "25",
		"AGENT_H2A_RENDER_MAX_BUFFERED_CHARS":     "256",
		"AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS":    "3",
		"AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH": "false",
		"AGENT_DEFAULT_REACT_MAX_STEPS":           "12",
		"AGENT_AUTOMATION_ENABLED":                "false",
		"AGENT_AUTOMATION_DEFAULT_ZONE_ID":        "Asia/Shanghai",
		"AGENT_AUTOMATION_POOL_SIZE":              "7",
		"LOGGING_AGENT_REQUEST_ENABLED":           "false",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Auth.Enabled {
			t.Fatalf("expected auth disabled from env")
		}
		if cfg.ResourceTicket.Secret != "secret" {
			t.Fatalf("unexpected resource ticket secret: %q", cfg.ResourceTicket.Secret)
		}
		if cfg.ResourceTicket.TTLSeconds != 300 {
			t.Fatalf("unexpected resource ticket ttl: %d", cfg.ResourceTicket.TTLSeconds)
		}
		if !cfg.Stream.IncludeToolPayloadEvents {
			t.Fatalf("expected stream tool payload flag enabled")
		}
		if !cfg.Stream.DebugEventsEnabled {
			t.Fatalf("expected stream debug event flag enabled")
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
		if cfg.Automation.Enabled {
			t.Fatalf("expected automation disabled")
		}
		if cfg.Automation.DefaultZoneID != "Asia/Shanghai" {
			t.Fatalf("unexpected automation default zone: %q", cfg.Automation.DefaultZoneID)
		}
		if cfg.Automation.PoolSize != 7 {
			t.Fatalf("unexpected automation pool size: %d", cfg.Automation.PoolSize)
		}
		if cfg.Logging.Request.Enabled {
			t.Fatalf("expected request logging disabled")
		}
	})
}

func TestLoadAcceptsDeltaLogsEnabledAlias(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"DELTA_LOGS_ENABLED": "true",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Stream.DebugEventsEnabled {
			t.Fatalf("expected DELTA_LOGS_ENABLED to enable stream debug events")
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

func TestLoadEnvOverridesAndBashYAMLConfig(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"CONTAINER_HUB_BASE_URL":               "http://127.0.0.1:18000",
		"AGENT_DEFAULT_BUDGET_HITL_TIMEOUT_MS": "60000",
	}, func() {
		content := "" +
			"working-directory: " + filepath.ToSlash(filepath.Join("var", "runtime")) + "\n" +
			"allowed-commands: pwd,echo\n" +
			"shell-features-enabled: true\n" +
			"shell-args:\n" +
			"  - -NoProfile\n" +
			"  - -Command\n" +
			"  - \"{{command}}\"\n" +
			"hitl-default-timeout-ms: 45000\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "bash.yml"), &content, func() {
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
				if cfg.BashHITL.DefaultTimeoutMs != 45000 {
					t.Fatalf("unexpected bash HITL timeout: %d", cfg.BashHITL.DefaultTimeoutMs)
				}
				if cfg.Defaults.Budget.Hitl.TimeoutMs != 60000 {
					t.Fatalf("unexpected default HITL budget timeout: %d", cfg.Defaults.Budget.Hitl.TimeoutMs)
				}
			})
		})
	})
}

func TestLoadBashShellArgsFromFile(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"shell-executable: powershell.exe\n" +
			"shell-args:\n" +
			"  - -NoProfile\n" +
			"  - -ExecutionPolicy\n" +
			"  - Bypass\n" +
			"  - -Command\n" +
			"  - \"{{command}}\"\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "bash.yml"), &content, func() {
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
	})
}

func TestDeprecatedBashPathConfigFailsStartup(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"working-directory: " + filepath.ToSlash(filepath.Join("var", "runtime")) + "\n" +
			"allowed-paths: [\".\", \"/tmp/example\"]\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "bash.yml"), &content, func() {
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "allowed-paths") {
					t.Fatalf("expected deprecated allowed-paths error, got %v", err)
				}
			})
		})
	})
}

func TestDeprecatedFileToolsPathConfigFailsStartup(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"allowed-read-paths:\n" +
			"  - /read/a\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), &content, func() {
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "allowed-read-paths") {
					t.Fatalf("expected deprecated allowed-read-paths error, got %v", err)
				}
			})
		})
	})
}

func TestAccessPolicyConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"version: 1\n" +
			"working-directory: \"@workspace\"\n" +
			"levels:\n" +
			"  default:\n" +
			"    read-roots:\n" +
			"      - \"@workspace\"\n" +
			"    write-roots:\n" +
			"      - \"@workspace\"\n" +
			"    readonly-roots: []\n" +
			"    approvals:\n" +
			"      read-outside-roots: block\n" +
			"      write-outside-roots: hitl\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "access-policy.yml"), &content, func() {
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
			})
		})
	})
}

func TestFileToolsConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"working-directory: " + filepath.ToSlash(filepath.Join("tmp", "files")) + "\n" +
			"max-read-bytes: 1234\n" +
			"max-write-bytes: 5678\n" +
			"max-batch-ops: 9\n" +
			"require-write-approval: false\n" +
			"require-read-before-write: false\n" +
			"read-before-write-scope: chat\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), &content, func() {
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
	})
}

func TestFileToolsConfigRejectsInvalidReadBeforeWriteScope(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "read-before-write-scope: global\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), &content, func() {
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "read-before-write-scope") {
					t.Fatalf("expected invalid read-before-write-scope error, got %v", err)
				}
			})
		})
	})
}

func TestFileToolsConfigLSPHookYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"hooks:\n" +
			"  after-file-change:\n" +
			"    lsp-diagnostics:\n" +
			"      enabled: false\n" +
			"      timeout-ms: 42\n" +
			"      languages: [\"go\", \"python\"]\n" +
			"      servers:\n" +
			"        go:\n" +
			"          command: custom-gopls\n" +
			"          args: [\"serve\"]\n"
		withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), &content, func() {
				cfg, err := Load()
				if err != nil {
					t.Fatalf("load config: %v", err)
				}
				lsp := cfg.FileTools.Hooks.AfterFileChange.LSPDiagnostics
				if lsp.Enabled {
					t.Fatalf("expected lsp diagnostics hook disabled from yaml")
				}
				if lsp.TimeoutMs != 42 {
					t.Fatalf("unexpected timeout: %d", lsp.TimeoutMs)
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
	})
}

func TestHostToolsConfigYAMLOverrides(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"access-policy:\n" +
			"  version: 1\n" +
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
			"  shell-timeout-ms: 12345\n" +
			"  max-command-chars: 4321\n" +
			"  hitl-default-timeout-ms: 45000\n" +
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
					withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), &content, func() {
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
						if cfg.Bash.WorkingDirectory != filepath.Join("var", "host") || cfg.Bash.ShellExecutable != "bash" || cfg.Bash.ShellTimeoutMs != 12345 || cfg.Bash.MaxCommandChars != 4321 {
							t.Fatalf("unexpected bash config: %#v", cfg.Bash)
						}
						if strings.Join(cfg.Bash.AllowedCommands, ",") != "pwd,echo" {
							t.Fatalf("unexpected allowed commands: %#v", cfg.Bash.AllowedCommands)
						}
						if cfg.BashHITL.DefaultTimeoutMs != 45000 {
							t.Fatalf("unexpected bash HITL timeout: %d", cfg.BashHITL.DefaultTimeoutMs)
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

func TestHostToolsConfigOverridesLegacyToolFiles(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		legacyAccess := "" +
			"levels:\n" +
			"  default:\n" +
			"    approvals:\n" +
			"      read-outside-roots: block\n"
		legacyBash := "" +
			"working-directory: legacy-bash\n" +
			"allowed-commands: pwd\n"
		legacyFileTools := "" +
			"working-directory: legacy-files\n" +
			"max-read-bytes: 111\n"
		merged := "" +
			"access-policy:\n" +
			"  levels:\n" +
			"    default:\n" +
			"      approvals:\n" +
			"        read-outside-roots: auto\n" +
			"bash:\n" +
			"  working-directory: merged-bash\n" +
			"file-tools:\n" +
			"  max-read-bytes: 222\n"
		withProjectFileContents(t, filepath.Join("configs", "access-policy.yml"), &legacyAccess, func() {
			withProjectFileContents(t, filepath.Join("configs", "bash.yml"), &legacyBash, func() {
				withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), &legacyFileTools, func() {
					withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), &merged, func() {
						cfg, err := Load()
						if err != nil {
							t.Fatalf("load config: %v", err)
						}
						if cfg.AccessPolicy.Levels["default"].Approvals.ReadOutsideRoots != "auto" {
							t.Fatalf("expected host-tools access policy to win, got %#v", cfg.AccessPolicy.Levels["default"].Approvals)
						}
						if cfg.Bash.WorkingDirectory != "merged-bash" {
							t.Fatalf("expected host-tools bash working dir to win, got %q", cfg.Bash.WorkingDirectory)
						}
						if strings.Join(cfg.Bash.AllowedCommands, ",") != "pwd" {
							t.Fatalf("expected legacy bash fallback to remain, got %#v", cfg.Bash.AllowedCommands)
						}
						if cfg.FileTools.WorkingDirectory != "legacy-files" {
							t.Fatalf("expected legacy file-tools fallback to remain, got %q", cfg.FileTools.WorkingDirectory)
						}
						if cfg.FileTools.MaxReadBytes != 222 {
							t.Fatalf("expected host-tools file-tools max read to win, got %d", cfg.FileTools.MaxReadBytes)
						}
					})
				})
			})
		})
	})
}

func TestHostToolsDeprecatedPathConfigFailsStartup(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		bashContent := "" +
			"bash:\n" +
			"  allowed-paths: [\".\"]\n"
		withProjectFileContents(t, filepath.Join("configs", "bash.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), &bashContent, func() {
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "allowed-paths") || !strings.Contains(err.Error(), "configs/host-tools.yml > access-policy") {
					t.Fatalf("expected deprecated allowed-paths error, got %v", err)
				}
			})
		})
		fileToolsContent := "" +
			"file-tools:\n" +
			"  allowed-read-paths: [\".\"]\n"
		withProjectFileContents(t, filepath.Join("configs", "file-tools.yml"), nil, func() {
			withProjectFileContents(t, filepath.Join("configs", "host-tools.yml"), &fileToolsContent, func() {
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), "allowed-read-paths") || !strings.Contains(err.Error(), "configs/host-tools.yml > access-policy") {
					t.Fatalf("expected deprecated allowed-read-paths error, got %v", err)
				}
			})
		})
	})
}

func TestLoadContainerHubDisabledWhenBaseURLMissing(t *testing.T) {
	withIsolatedEnv(t, nil, func() {
		content := "" +
			"auth-token:\n" +
			"default-environment-id:\n" +
			"request-timeout-ms: 300000\n" +
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

func TestLoadLLMChatRecordFromDebugEnv(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"DEBUG_LLM_CHAT_RECORD": "true",
	}, func() {
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Logging.LLMInteraction.RecordEnabled {
			t.Fatalf("expected llm chat record enabled from env")
		}
		if cfg.Logging.LLMInteraction.RecordDir != filepath.Join("runtime", "chats", "llm") {
			t.Fatalf("unexpected llm chat record dir: %q", cfg.Logging.LLMInteraction.RecordDir)
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
	cfg := defaultConfig()
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
	if err := cfg.normalize(); err == nil {
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
		"PAN_DIR": missing,
	}, func() {
		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail for missing PAN_DIR")
		}
		if !strings.Contains(err.Error(), "PAN_DIR does not exist: "+missing) {
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
		"PAN_DIR": panFile,
	}, func() {
		_, err := Load()
		if err == nil {
			t.Fatal("expected Load() to fail for file PAN_DIR")
		}
		if !strings.Contains(err.Error(), "PAN_DIR is not a directory: "+panFile) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func withIsolatedEnv(t *testing.T, values map[string]string, fn func()) {
	t.Helper()

	keys := []string{
		"SERVICE_CONFIG_DIR",
		"SERVICE_DATA_DIR",
		"RUNTIME_DIR",
		"SERVER_PORT",
		"REGISTRIES_DIR",
		"OWNER_DIR",
		"AGENTS_DIR",
		"TEAMS_DIR",
		"ROOT_DIR",
		"AUTOMATIONS_DIR",
		"CHATS_DIR",
		"MEMORY_DIR",
		"PAN_DIR",
		"SKILLS_MARKET_DIR",
		"CONTAINER_HUB_BASE_URL",
		"CONTAINER_HUB_AUTH_TOKEN",
		"CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID",
		"CONTAINER_HUB_REQUEST_TIMEOUT_MS",
		"CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL",
		"CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS",
		"CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS",
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
		"AUTH_ENABLED",
		"AUTH_LOCAL_PUBLIC_KEY_FILE",
		"AUTH_JWKS_URI",
		"AUTH_ISSUER",
		"AUTH_JWKS_CACHE_SECONDS",
		"CHAT_RESOURCE_TICKET_SECRET",
		"CHAT_RESOURCE_TICKET_TTL_SECONDS",
		"STREAM_INCLUDE_TOOL_PAYLOAD_EVENTS",
		"DEBUG_EVENTS_ENABLED",
		"AGENT_SSE_HEARTBEAT_INTERVAL_MS",
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
		"AGENT_DEFAULT_MAX_TOKENS",
		"AGENT_DEFAULT_BUDGET_RUN_TIMEOUT_MS",
		"AGENT_DEFAULT_BUDGET_MAX_STEPS",
		"AGENT_DEFAULT_BUDGET_MODEL_MAX_CALLS",
		"AGENT_DEFAULT_BUDGET_MODEL_TIMEOUT_MS",
		"AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT",
		"AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS",
		"AGENT_DEFAULT_BUDGET_TOOL_TIMEOUT_MS",
		"AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT",
		"AGENT_DEFAULT_BUDGET_HITL_TIMEOUT_MS",
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
