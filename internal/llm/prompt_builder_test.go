package llm

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

func TestBuildSystemPromptPlacesStaticMemoryBeforeRuntimeMemory(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:             "demo",
		AgentName:            "Demo",
		AgentRole:            "Prompt Tester",
		AgentDescription:     "Verifies prompt ordering",
		Mode:                 "REACT",
		SoulPrompt:           "soul",
		StaticMemoryPrompt:   "static-memory",
		AgentHasMemoryConfig: true,
		StableMemoryContext:  "Runtime Context: Stable Memory\n- stable-fact",
	}, api.QueryRequest{}, "", PromptBuildOptions{})

	staticIndex := strings.Index(prompt, "static-memory")
	runtimeIndex := strings.Index(prompt, "Runtime Context: Stable Memory")
	if staticIndex < 0 || runtimeIndex < 0 {
		t.Fatalf("expected both static and runtime memory sections, got %q", prompt)
	}
	if staticIndex > runtimeIndex {
		t.Fatalf("expected static memory before runtime memory, got %q", prompt)
	}
}

func TestBuildSystemPromptInjectsAgentIdentityWithoutSoulIdentity(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:         "demo",
		AgentName:        "Demo",
		AgentRole:        "Prompt Tester",
		AgentDescription: "Verifies identity injection",
		Mode:             "REACT",
		SoulPrompt:       "# Soul\n\n## Persona\n\nOnly behavior lives here.",
	}, api.QueryRequest{}, "", PromptBuildOptions{})

	for _, expected := range []string{
		"Agent Identity",
		"key: demo",
		"name: Demo",
		"role: Prompt Tester",
		"description: Verifies identity injection",
		"mode: REACT",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected %q in prompt, got %q", expected, prompt)
		}
	}
	if strings.Count(prompt, "Agent Identity") != 1 {
		t.Fatalf("expected a single agent identity section, got %q", prompt)
	}
}

func TestBuildSystemPromptAddsCoderSystemPromptOnlyForMainCoderStage(t *testing.T) {
	session := QuerySession{
		AgentKey:          "coder",
		AgentName:         "Coder",
		Mode:              "CODER",
		CoderSystemPrompt: "main coder system prompt",
	}
	main := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{Stage: "coder"})
	if !strings.Contains(main, "main coder system prompt") {
		t.Fatalf("expected CODER main prompt to include coder system prompt, got %q", main)
	}
	planning := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{Stage: "coder-plan"})
	if strings.Contains(planning, "main coder system prompt") {
		t.Fatalf("expected CODER planning prompt to skip coder system prompt, got %q", planning)
	}
	execute := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{Stage: "coder-execute"})
	if strings.Contains(execute, "main coder system prompt") {
		t.Fatalf("expected CODER execute prompt to skip coder system prompt, got %q", execute)
	}
}

func TestBuildSystemPromptRendersCoderSystemPromptPlaceholders(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:          "coder",
		AgentName:         "Coder",
		Mode:              "CODER",
		PlanningMode:      false,
		ToolNames:         []string{"bash", "file_read", "file_write", "file_edit", "ask_user_question"},
		CoderSystemPrompt: "CODER {{agent_key}} {{agent_name}} {{planning_mode}} {{workspace_dir}} {{available_tools}} {{plan_stage_tools}} {{execute_stage_tools}} {{file_read_tool_name}} {{ask_user_question_tool_name}}",
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{WorkspaceDir: "/workspace"},
		},
	}, api.QueryRequest{Message: "hello"}, "", PromptBuildOptions{
		Stage: "coder",
		ToolDefinitions: []api.ToolDetailResponse{
			{Name: "bash"},
			{Name: "file_read"},
			{Name: "file_write"},
			{Name: "file_edit"},
			{Name: "ask_user_question"},
		},
	})

	for _, expected := range []string{
		"CODER coder Coder false /workspace",
		"bash, file_read, file_write, file_edit, ask_user_question",
		"file_read, file_glob, file_grep, datetime, regex, vision_recognize, ask_user_question, finalize_planning",
		"bash, file_read, file_write, file_edit",
		"file_read ask_user_question",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected %q in rendered CODER prompt, got %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "}}") {
		t.Fatalf("expected CODER system placeholders to be rendered, got %q", prompt)
	}
}

func TestBuildSystemPromptPlacesAgentIdentityBeforeSoul(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:   "demo",
		AgentName:  "Demo",
		AgentRole:  "Prompt Tester",
		Mode:       "REACT",
		SoulPrompt: "# Soul\n\n## Persona\n\nStay calm.",
	}, api.QueryRequest{}, "", PromptBuildOptions{})

	identityIndex := strings.Index(prompt, "Agent Identity")
	soulIndex := strings.Index(prompt, "# Soul")
	if identityIndex < 0 || soulIndex < 0 {
		t.Fatalf("expected both agent identity and soul sections, got %q", prompt)
	}
	if identityIndex > soulIndex {
		t.Fatalf("expected agent identity before soul, got %q", prompt)
	}
}

func TestBuildSystemPromptIncludesAgentAndWorkspacePromptsInOrder(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:              "demo",
		ChatID:                "chat-1",
		RunID:                 "run-1",
		Mode:                  "CODER",
		AgentsPrompt:          "agent directory rules",
		WorkspaceAgentsPrompt: "workspace project rules",
		ContextTags:           []string{"session"},
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{WorkspaceDir: "/workspace"},
		},
	}, api.QueryRequest{ChatID: "chat-1", RunID: "run-1"}, "", PromptBuildOptions{})

	agentIndex := strings.Index(prompt, "agent directory rules")
	workspaceTitleIndex := strings.Index(prompt, "Workspace AGENTS.md")
	workspaceIndex := strings.Index(prompt, "workspace project rules")
	runtimeIndex := strings.Index(prompt, "Runtime Context: Session")
	if agentIndex < 0 || workspaceTitleIndex < 0 || workspaceIndex < 0 || runtimeIndex < 0 {
		t.Fatalf("expected agent, workspace, and runtime sections, got %q", prompt)
	}
	if !(agentIndex < workspaceTitleIndex && workspaceTitleIndex < workspaceIndex && workspaceIndex < runtimeIndex) {
		t.Fatalf("unexpected prompt ordering:\n%s", prompt)
	}
}

func TestBuildSystemPromptPreservesConfiguredProjectPromptTitles(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey: "demo",
		ChatID:   "chat-1",
		RunID:    "run-1",
		Mode:     "CODER",
		WorkspaceAgentsPrompt: "Workspace AGENTS.md\nworkspace rules\n\n" +
			"Agent-managed Project project/AGENTS.md\nagent-managed rules",
	}, api.QueryRequest{ChatID: "chat-1", RunID: "run-1"}, "", PromptBuildOptions{})

	if strings.Count(prompt, "Workspace AGENTS.md") != 1 {
		t.Fatalf("expected configured workspace title once, got %q", prompt)
	}
	if !strings.Contains(prompt, "Agent-managed Project project/AGENTS.md") {
		t.Fatalf("expected agent-managed project title, got %q", prompt)
	}
}

func TestBuildSystemPromptKeepsIdentityWhenSoulIsMissing(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:         "demo",
		AgentName:        "Demo",
		AgentRole:        "Prompt Tester",
		AgentDescription: "No soul file present",
		Mode:             "REACT",
	}, api.QueryRequest{}, "", PromptBuildOptions{})

	for _, expected := range []string{
		"Agent Identity",
		"key: demo",
		"name: Demo",
		"role: Prompt Tester",
		"description: No soul file present",
		"mode: REACT",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected %q in prompt, got %q", expected, prompt)
		}
	}
}

func TestBuildMemorySectionAvoidsDuplicatingLayeredHeaders(t *testing.T) {
	section := buildMemorySection(QuerySession{
		StableMemoryContext: "Runtime Context: Stable Memory\n- stable-fact",
		ObservationContext:  "Runtime Context: Relevant Observations\n- recent-observation",
		WorkflowContext:     "Runtime Context: Related Workflows\nworkflow: deploy automation",
	}, api.QueryRequest{})

	if strings.Count(section, "Runtime Context: Stable Memory") != 1 {
		t.Fatalf("expected one stable memory header, got %q", section)
	}
	if strings.Count(section, "Runtime Context: Relevant Observations") != 1 {
		t.Fatalf("expected one observation header, got %q", section)
	}
}

func TestBuildRuntimeContextPromptAutoIncludesSandboxSection(t *testing.T) {
	prompt := buildRuntimeContextPrompt(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			SandboxContext: &SandboxContext{
				EnvironmentID:     "browser",
				Level:             "RUN",
				EnvironmentPrompt: "Use the browser sandbox carefully.",
			},
		},
	}, api.QueryRequest{})

	if !strings.Contains(prompt, "Runtime Context: Sandbox") {
		t.Fatalf("expected sandbox section in prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "environmentId: browser") {
		t.Fatalf("expected sandbox environment details in prompt, got %q", prompt)
	}
}

func TestBuildRuntimeContextPromptAutoIncludesMemorySection(t *testing.T) {
	prompt := buildRuntimeContextPrompt(QuerySession{
		AgentHasMemoryConfig: true,
		StableMemoryContext:  "Runtime Context: Stable Memory\n- stable-fact",
	}, api.QueryRequest{})

	if !strings.Contains(prompt, "Runtime Context: Stable Memory") {
		t.Fatalf("expected memory section in prompt, got %q", prompt)
	}
}

func TestBuildRuntimeContextPromptSkipsMemoryFallbackWithoutMemoryConfig(t *testing.T) {
	prompt := buildRuntimeContextPrompt(QuerySession{}, api.QueryRequest{
		Params: map[string]any{
			"memoryContext": "legacy-memory",
		},
	})

	if strings.Contains(prompt, "Runtime Context: Agent Memory") {
		t.Fatalf("expected memory fallback to stay gated by memory config, got %q", prompt)
	}
}

func TestBuildRuntimeContextPromptIgnoresDesktopParams(t *testing.T) {
	prompt := buildRuntimeContextPrompt(QuerySession{}, api.QueryRequest{
		Params: map[string]any{
			"desktop": map[string]any{
				"source":          "copilot",
				"route":           "/settings?section=navigation",
				"pageKey":         "native:/settings?section=navigation",
				"pageKind":        "native",
				"snapshotVersion": 3,
				"snapshotAt":      "2026-05-16T12:00:00Z",
				"pageContext": map[string]any{
					"title": "Bing",
					"url":   "https://www.bing.com/",
				},
			},
		},
	})

	for _, unexpected := range []string{
		"Runtime Context: ZenMind Desktop",
		"desktop_action",
		"desktop_cdp",
		"pageContext is only a snapshot",
		"Use desktop_action for Desktop shell",
		"Use desktop_cdp for current page",
		"Use desktop_cdp when the task depends on current live Desktop page state.",
		"route: /settings?section=navigation",
		"pageKey: native:/settings?section=navigation",
		"pageKind: native",
		"snapshotVersion: 3",
		"currentPageTitle: Bing",
		"currentPageUrl: https://www.bing.com/",
	} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("did not expect desktop context %q in prompt, got %q", unexpected, prompt)
		}
	}
}

func TestBuildSessionSectionMergesContextAndAuth(t *testing.T) {
	section := buildSessionSection(QuerySession{
		ChatID:    "chat-1",
		RunID:     "run-1",
		RequestID: "req-1",
		RuntimeContext: RuntimeRequestContext{
			TeamID:    "team-1",
			LocalMode: false,
			Scene:     &api.Scene{Title: "ZenMind", URL: "https://example.com"},
			AuthIdentity: &AuthIdentity{
				Subject:   "user-1",
				DeviceID:  "device-1",
				Scope:     "chat:write",
				IssuedAt:  "2026-04-23T09:00:00Z",
				ExpiresAt: "2026-04-23T10:00:00Z",
			},
			References: []api.Reference{
				{ID: "ref-1", Name: "doc.md", SandboxPath: "/workspace/doc.md"},
			},
			LocalPaths: LocalPaths{
				WorkingDirectory: "/Users/linlay/Project/zenmind/agent-platform",
				RootDir:          "/Users/linlay/Project/zenmind/zenmind-env",
				PanDir:           "/Users/linlay/Project/zenmind/pan",
				AgentDir:         "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
				OwnerDir:         "/Users/linlay/Project/zenmind/zenmind-env/owner",
				MemoryDir:        "/Users/linlay/Project/zenmind/zenmind-env/memory",
				SkillsDir:        "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi/skills",
				SkillsMarketDir:  "/Users/linlay/Project/zenmind/zenmind-env/skills-market",
			},
			SandboxPaths: SandboxPaths{
				WorkspaceDir: "/workspace",
				RootDir:      "/root",
				PanDir:       "/pan",
				AgentDir:     "/agent",
				OwnerDir:     "/owner",
				MemoryDir:    "/memory",
				SkillsDir:    "/skills",
			},
		},
	}, api.QueryRequest{})

	if !strings.Contains(section, "Runtime Context: Session") {
		t.Fatalf("expected session header, got %q", section)
	}
	if !strings.Contains(section, "chatId: chat-1") {
		t.Fatalf("expected chatId in session section, got %q", section)
	}
	if strings.Contains(section, "runId:") || strings.Contains(section, "requestId:") {
		t.Fatalf("expected session section to exclude volatile run identifiers, got %q", section)
	}
	if !strings.Contains(section, "teamId: team-1") || !strings.Contains(section, "scene: title=ZenMind, url=https://example.com") {
		t.Fatalf("expected team and scene in session section, got %q", section)
	}
	for _, expected := range []string{
		"subject: user-1",
		"deviceId: device-1",
		"scope: chat:write",
		"issuedAt: 2026-04-23T09:00:00Z",
		"expiresAt: 2026-04-23T10:00:00Z",
	} {
		if !strings.Contains(section, expected) {
			t.Fatalf("expected auth identity field %q in session section, got %q", expected, section)
		}
	}
	if !strings.Contains(section, "references:") || !strings.Contains(section, "id: ref-1") {
		t.Fatalf("expected references in session section, got %q", section)
	}
	if strings.Contains(section, "workspace_dir:") || strings.Contains(section, "chat_dir:") || strings.Contains(section, "agent_dir:") {
		t.Fatalf("expected session section to exclude path fields, got %q", section)
	}
	assertOrderedSubstrings(t, section, []string{
		"chatId:",
		"teamId:",
		"scene:",
		"subject:",
		"deviceId:",
		"scope:",
		"issuedAt:",
		"expiresAt:",
		"references:",
	})
}

func TestBuildSystemEnvironmentSectionUsesLocalPathsWithoutSandbox(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		ChatID:    "chat-1",
		RunID:     "run-1",
		RequestID: "req-1",
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			LocalPaths: LocalPaths{
				WorkspaceDir:       "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
				WorkingDirectory:   "/Users/linlay/Project/zenmind/agent-platform",
				ChatAttachmentsDir: "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
				RootDir:            "/Users/linlay/Project/zenmind/zenmind-env/root",
				SkillsDir:          "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi/skills",
				AgentDir:           "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
				OwnerDir:           "/Users/linlay/Project/zenmind/zenmind-env/owner",
				SkillsMarketDir:    "/Users/linlay/Project/zenmind/zenmind-env/skills-market",
				AgentsDir:          "/Users/linlay/Project/zenmind/zenmind-env/agents",
				TeamsDir:           "/Users/linlay/Project/zenmind/zenmind-env/teams",
				AutomationsDir:     "/Users/linlay/Project/zenmind/zenmind-env/automations",
				ChatsDir:           "/Users/linlay/Project/zenmind/zenmind-env/chats",
				MemoryDir:          "/Users/linlay/Project/zenmind/zenmind-env/memory",
				ModelsDir:          "/Users/linlay/Project/zenmind/zenmind-env/registries/models",
				ProvidersDir:       "/Users/linlay/Project/zenmind/zenmind-env/registries/providers",
				MCPServersDir:      "/Users/linlay/Project/zenmind/zenmind-env/registries/mcp-servers",
				ViewportServersDir: "/Users/linlay/Project/zenmind/zenmind-env/registries/viewport-servers",
				ToolsDir:           "/Users/linlay/Project/zenmind/zenmind-env/tools",
				ViewportsDir:       "/Users/linlay/Project/zenmind/zenmind-env/viewports",
				PanDir:             "/Users/linlay/Server/zenmind-pan",
			},
			SandboxPaths: SandboxPaths{
				WorkspaceDir: "/workspace",
				RootDir:      "/root",
				AgentDir:     "/agent",
			},
		},
	})

	if !strings.Contains(section, "Runtime Context: System Environment") {
		t.Fatalf("expected system environment header, got %q", section)
	}
	if !strings.Contains(section, "workspace_dir: /Users/linlay/Project/zenmind/zenmind-env/chats/chat-1") {
		t.Fatalf("expected chat workspace path in system environment, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /Users/linlay/Project/zenmind/zenmind-env/chats/chat-1") {
		t.Fatalf("expected chat dir in system environment, got %q", section)
	}
	if strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected local paths instead of sandbox paths, got %q", section)
	}
	if strings.Contains(section, "tools_dir:") || strings.Contains(section, "viewports_dir:") {
		t.Fatalf("expected tools_dir and viewports_dir to be removed, got %q", section)
	}
	if strings.Contains(section, "datetime:") {
		t.Fatalf("expected system environment to exclude datetime, got %q", section)
	}
	if strings.Contains(section, "chatId:") || strings.Contains(section, "runId:") || strings.Contains(section, "requestId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", section)
	}
	assertOrderedSubstrings(t, section, []string{
		"workspace_dir:",
		"chat_dir:",
		"root_dir:",
		"skills_dir:",
		"agent_dir:",
		"owner_dir:",
	})
	assertLastField(t, section, "pan_dir:")
}

func TestBuildSystemEnvironmentSectionSeparatesExplicitWorkspaceAndChatDir(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{
				WorkspaceDir:       "/",
				WorkingDirectory:   "/",
				ChatAttachmentsDir: "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
			},
		},
	})

	if !strings.Contains(section, "workspace_dir: / # 工具默认工作目录 / 权限工作根") {
		t.Fatalf("expected explicit workspace root in system environment, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /Users/linlay/Project/zenmind/zenmind-env/chats/chat-1") {
		t.Fatalf("expected separate chat dir in system environment, got %q", section)
	}
	if strings.Contains(section, "workspace_dir: /Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected process cwd not to be used as workspace_dir, got %q", section)
	}
}

func TestBuildSystemEnvironmentSectionUsesSandboxPathsWhenSandboxEnabled(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			LocalPaths: LocalPaths{
				ChatAttachmentsDir: "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
				AgentDir:           "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
			},
			SandboxPaths: SandboxPaths{
				WorkspaceDir:       "/workspace",
				RootDir:            "/root",
				SkillsDir:          "/skills",
				AgentDir:           "/agent",
				OwnerDir:           "/owner",
				SkillsMarketDir:    "/skills-market",
				AgentsDir:          "/agents",
				TeamsDir:           "/teams",
				AutomationsDir:     "/automations",
				ChatsDir:           "/chats",
				MemoryDir:          "/memory",
				ModelsDir:          "/models",
				ProvidersDir:       "/providers",
				MCPServersDir:      "/mcp-servers",
				ViewportServersDir: "/viewport-servers",
				ToolsDir:           "/tools",
				ViewportsDir:       "/viewports",
				PanDir:             "/pan",
			},
		},
	})

	if !strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected sandbox workspace dir in system environment, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /workspace") {
		t.Fatalf("expected sandbox chat dir in system environment, got %q", section)
	}
	if strings.Contains(section, "/Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected sandbox paths to win when sandbox is enabled, got %q", section)
	}
	if strings.Contains(section, "tools_dir:") || strings.Contains(section, "viewports_dir:") {
		t.Fatalf("expected tools_dir and viewports_dir to be removed, got %q", section)
	}
	if strings.Contains(section, "datetime:") {
		t.Fatalf("expected system environment to exclude datetime, got %q", section)
	}
	if strings.Contains(section, "chatId:") || strings.Contains(section, "runId:") || strings.Contains(section, "requestId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", section)
	}
	assertOrderedSubstrings(t, section, []string{
		"workspace_dir:",
		"chat_dir:",
		"root_dir:",
		"skills_dir:",
		"agent_dir:",
		"owner_dir:",
	})
	assertLastField(t, section, "pan_dir:")
}

func TestBuildSystemEnvironmentSectionOmitsSkillsMarketByDefault(t *testing.T) {
	localSection := buildSystemEnvironmentSection(QuerySession{
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{
				WorkingDirectory: "/workspace/local",
				AgentDir:         "/agents/demo",
				SkillsDir:        "/agents/demo/skills",
			},
		},
	})
	if strings.Contains(localSection, "skills_market_dir:") {
		t.Fatalf("expected local system environment to omit skills_market_dir, got %q", localSection)
	}

	sandboxSection := buildSystemEnvironmentSection(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			SandboxPaths: SandboxPaths{
				WorkspaceDir: "/workspace",
				AgentDir:     "/agent",
				SkillsDir:    "/skills",
			},
		},
	})
	if strings.Contains(sandboxSection, "skills_market_dir:") {
		t.Fatalf("expected sandbox system environment to omit skills_market_dir, got %q", sandboxSection)
	}
}

func TestBuildSystemEnvironmentSectionIncludesExplicitSkillsMarket(t *testing.T) {
	localSection := buildSystemEnvironmentSection(QuerySession{
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{
				WorkingDirectory: "/workspace/local",
				SkillsMarketDir:  "/runtime/skills-market",
			},
		},
	})
	if !strings.Contains(localSection, "skills_market_dir: /runtime/skills-market") {
		t.Fatalf("expected explicit local skills_market_dir, got %q", localSection)
	}

	sandboxSection := buildSystemEnvironmentSection(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			SandboxPaths: SandboxPaths{
				WorkspaceDir:    "/workspace",
				SkillsMarketDir: "/skills-market",
			},
		},
	})
	if !strings.Contains(sandboxSection, "skills_market_dir: /skills-market") {
		t.Fatalf("expected explicit sandbox skills_market_dir, got %q", sandboxSection)
	}
}

func TestBuildSystemPromptSeparatesSystemEnvironmentAndSessionContext(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		ChatID:      "chat-1",
		RunID:       "run-1",
		RequestID:   "req-1",
		ContextTags: []string{"system", "session"},
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			TeamID:    "team-1",
			LocalPaths: LocalPaths{
				WorkspaceDir:       "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
				WorkingDirectory:   "/Users/linlay/Project/zenmind/agent-platform",
				ChatAttachmentsDir: "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
				AgentDir:           "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
			},
			SandboxPaths: SandboxPaths{
				WorkspaceDir: "/workspace",
				AgentDir:     "/agent",
			},
		},
	}, api.QueryRequest{}, "", PromptBuildOptions{})

	systemIndex := strings.Index(prompt, "Runtime Context: System Environment")
	sessionIndex := strings.Index(prompt, "Runtime Context: Session")
	if systemIndex < 0 || sessionIndex < 0 {
		t.Fatalf("expected both system environment and session sections, got %q", prompt)
	}
	if strings.Contains(prompt, "Runtime Context: Context") {
		t.Fatalf("expected old context header to be removed, got %q", prompt)
	}
	if !strings.Contains(prompt, "workspace_dir: /Users/linlay/Project/zenmind/zenmind-env/chats/chat-1") {
		t.Fatalf("expected final prompt to include chat workspace dir, got %q", prompt)
	}
	if !strings.Contains(prompt, "chat_dir: /Users/linlay/Project/zenmind/zenmind-env/chats/chat-1") {
		t.Fatalf("expected final prompt to include chat dir, got %q", prompt)
	}
	if !strings.Contains(prompt, "chatId: chat-1") {
		t.Fatalf("expected final prompt to include chatId, got %q", prompt)
	}
	if strings.Contains(prompt, "runId:") || strings.Contains(prompt, "requestId:") {
		t.Fatalf("expected final prompt to exclude volatile run identifiers, got %q", prompt)
	}
	sessionSection := prompt[sessionIndex:]
	if strings.Contains(sessionSection, "workspace_dir:") || strings.Contains(sessionSection, "chat_dir:") {
		t.Fatalf("expected session section to exclude workspace paths, got %q", sessionSection)
	}
	systemSection := prompt[systemIndex:sessionIndex]
	if strings.Contains(systemSection, "chatId:") || strings.Contains(systemSection, "runId:") || strings.Contains(systemSection, "requestId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", systemSection)
	}
	if strings.Contains(systemSection, "tools_dir:") || strings.Contains(systemSection, "viewports_dir:") {
		t.Fatalf("expected system environment to exclude tools_dir and viewports_dir, got %q", systemSection)
	}
	if strings.Contains(systemSection, "datetime:") {
		t.Fatalf("expected system environment to exclude datetime, got %q", systemSection)
	}
}

func TestBuildToolAppendixIncludesOnlyAfterCallHints(t *testing.T) {
	appendix := buildToolAppendix([]api.ToolDetailResponse{
		{
			Name:          "z_tool",
			Description:   "z description",
			AfterCallHint: "z hint",
			Meta:          map[string]any{"kind": "frontend"},
		},
		{
			Name:          "a_tool",
			Description:   "a description",
			AfterCallHint: "a hint",
			Meta:          map[string]any{"kind": "mcp"},
		},
	}, DefaultPromptAppendConfig(), true)

	if !strings.Contains(appendix, "工具调用后推荐指令:") {
		t.Fatalf("expected after-call hint title, got %q", appendix)
	}
	if !strings.Contains(appendix, "- a_tool: a hint") || !strings.Contains(appendix, "- z_tool: z hint") {
		t.Fatalf("expected hint lines, got %q", appendix)
	}
	if strings.Contains(appendix, "工具说明:") || strings.Contains(appendix, "a description") || strings.Contains(appendix, "[frontend]") || strings.Contains(appendix, "[mcp]") {
		t.Fatalf("expected descriptions and kinds to be omitted, got %q", appendix)
	}
	if strings.Index(appendix, "- a_tool: a hint") > strings.Index(appendix, "- z_tool: z hint") {
		t.Fatalf("expected appendix lines sorted by tool name, got %q", appendix)
	}
}

func TestBuildToolAppendixReturnsEmptyWithoutHints(t *testing.T) {
	appendix := buildToolAppendix([]api.ToolDetailResponse{
		{
			Name:        "demo",
			Description: "demo description",
		},
	}, DefaultPromptAppendConfig(), true)
	if appendix != "" {
		t.Fatalf("expected empty appendix when no hints exist, got %q", appendix)
	}
}

func TestBuildToolAppendixReturnsEmptyWhenAfterCallHintsDisabled(t *testing.T) {
	appendix := buildToolAppendix([]api.ToolDetailResponse{
		{
			Name:          "demo",
			AfterCallHint: "demo hint",
		},
	}, DefaultPromptAppendConfig(), false)
	if appendix != "" {
		t.Fatalf("expected empty appendix when hints are disabled, got %q", appendix)
	}
}

func assertOrderedSubstrings(t *testing.T, s string, items []string) {
	t.Helper()

	last := -1
	for _, item := range items {
		idx := strings.Index(s, item)
		if idx < 0 {
			t.Fatalf("expected %q in %q", item, s)
		}
		if idx <= last {
			t.Fatalf("expected %q after prior fields in %q", item, s)
		}
		last = idx
	}
}

func assertLastField(t *testing.T, s string, field string) {
	t.Helper()

	idx := strings.LastIndex(s, field)
	if idx < 0 {
		t.Fatalf("expected %q in %q", field, s)
	}
	if strings.Contains(s[idx+len(field):], "_dir:") {
		t.Fatalf("expected %q to be the last directory field in %q", field, s)
	}
}
