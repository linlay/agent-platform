package llm

import (
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/knowledge"
)

func runtimeSystemPromptForTest(session QuerySession) string {
	sections := []systemPromptSection{}
	appendRuntimeSystemPromptSections(&sections, session)
	contents := make([]string, 0, len(sections))
	for _, section := range sections {
		contents = append(contents, section.Content)
	}
	return strings.Join(contents, "\n\n")
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
	if strings.Contains(prompt, "当前智能体") || strings.Contains(prompt, "本智能体") || strings.Contains(prompt, "你自己") {
		t.Fatalf("expected agent identity to contain facts only, got %q", prompt)
	}
	if strings.Count(prompt, "Agent Identity") != 1 {
		t.Fatalf("expected a single agent identity section, got %q", prompt)
	}
}

func TestBuildSystemPromptIncludesStableReferenceProtocol(t *testing.T) {
	session := QuerySession{
		AgentKey: "demo",
		Mode:     "REACT",
		RuntimeContext: RuntimeRequestContext{
			References: []api.Reference{{ID: "r01", Name: "dynamic.csv"}},
		},
	}
	if prompt := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{}); strings.Contains(prompt, "[References]") {
		t.Fatalf("expected no reference protocol without configured prompt, got %q", prompt)
	}
	session.PromptAppend.Reference.ProtocolPrompt = "configured reference protocol"
	prompt := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{})

	if !strings.Contains(prompt, "configured reference protocol") {
		t.Fatalf("expected configured reference protocol in system prompt, got %q", prompt)
	}
	if strings.Contains(prompt, "dynamic.csv") || strings.Contains(prompt, "id: r01") {
		t.Fatalf("expected dynamic references to be excluded from system prompt, got %q", prompt)
	}
}

func TestBuildSystemPromptIncludesWorkspaceLessPathPolicy(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:           "demo",
		Mode:               "REACT",
		SkillCatalogPrompt: "skillId: demo\npath: @skills/demo/SKILL.md",
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{ChatDir: "/runtime/chats/chat-1"},
		},
	}, api.QueryRequest{}, "", PromptBuildOptions{
		ToolDefinitions: []api.ToolDetailResponse{
			{Name: "bash"},
			{Name: "file_read"},
			{Name: "file_glob"},
		},
	})

	for _, expected := range []string{
		"Runtime Context: Path Policy",
		"Workspace is unavailable",
		`cwd: "@chat"`,
		`explicit path, normally "@chat"`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected workspace-less prompt to contain %q, got %q", expected, prompt)
		}
	}
	// Skill loading rules come only from shared.skill.instructions-prompt.
	if strings.Contains(prompt, "Load an applicable skill") || strings.Contains(prompt, "Do not search or traverse directories") {
		t.Fatalf("did not expect source-owned skill loading rule in path policy, got %q", prompt)
	}
}

func TestBuildSystemPromptSkipsWorkspaceLessPathPolicyWithWorkspace(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:      "demo",
		Mode:          "REACT",
		WorkspaceRoot: "/workspace",
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{WorkspaceDir: "/workspace", ChatDir: "/runtime/chats/chat-1"},
		},
	}, api.QueryRequest{}, "", PromptBuildOptions{
		ToolDefinitions: []api.ToolDetailResponse{{Name: "bash"}},
	})

	if strings.Contains(prompt, "Runtime Context: Path Policy") {
		t.Fatalf("did not expect workspace-less path policy for Workspace session, got %q", prompt)
	}
}

func TestBuildSystemPromptIncludesCurrentPlanTasksWhenPlanToolsAvailable(t *testing.T) {
	context := "Runtime Context: Current Plan Tasks\nplanId: old_plan\ntasks:\n- task_1 | in_progress | resume"
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:        "coder",
		Mode:            "CODER",
		PlanTaskContext: context,
	}, api.QueryRequest{}, "", PromptBuildOptions{
		Stage: "coder",
		ToolDefinitions: []api.ToolDetailResponse{
			{Name: PlanGetTasksToolName},
			{Name: "bash"},
		},
	})

	if !strings.Contains(prompt, context) {
		t.Fatalf("expected current plan task context in prompt, got %q", prompt)
	}
}

func TestBuildSystemPromptSkipsCurrentPlanTasksForPlanningStage(t *testing.T) {
	context := "Runtime Context: Current Plan Tasks\nplanId: old_plan\ntasks:\n- task_1 | in_progress | resume"
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:        "coder",
		Mode:            "CODER",
		PlanTaskContext: context,
	}, api.QueryRequest{}, "", PromptBuildOptions{
		Stage: "coder-planning",
		ToolDefinitions: []api.ToolDetailResponse{
			{Name: PlanGetTasksToolName},
		},
	})

	if strings.Contains(prompt, context) {
		t.Fatalf("did not expect current plan task context in planning prompt, got %q", prompt)
	}
}

func TestBuildSystemPromptIncludesAdvancedUserPromptProtocolWhenEnabled(t *testing.T) {
	session := QuerySession{
		AgentKey:           "demo",
		Mode:               "REACT",
		AdvancedUserPrompt: true,
	}
	if prompt := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{}); strings.Contains(prompt, "advanced") {
		t.Fatalf("expected no advanced protocol without configured prompt, got %q", prompt)
	}
	session.PromptAppend.Reference.ProtocolPrompt = "plain reference protocol"
	session.PromptAppend.Reference.AdvancedProtocolPrompt = "advanced reference protocol"
	prompt := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{})
	if !strings.Contains(prompt, "advanced reference protocol") || !strings.Contains(prompt, "plain reference protocol") {
		t.Fatalf("expected both reference protocols when advanced user prompt is enabled, got %q", prompt)
	}

	session.AdvancedUserPrompt = false
	disabled := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{})
	if strings.Contains(disabled, "advanced reference protocol") || !strings.Contains(disabled, "plain reference protocol") {
		t.Fatalf("expected only the plain reference protocol when disabled, got %q", disabled)
	}
}

func TestBuildSystemPromptAddsCoderSystemPromptOnlyForMainCoderStage(t *testing.T) {
	session := QuerySession{
		AgentKey:         "coder",
		AgentName:        "Coder",
		Mode:             "CODER",
		ModeSystemPrompt: "main coder system prompt",
	}
	main := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{Stage: "coder"})
	if !strings.Contains(main, "main coder system prompt") {
		t.Fatalf("expected CODER main prompt to include coder system prompt, got %q", main)
	}
	planning := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{Stage: "coder-planning"})
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
		AgentKey:                "coder",
		AgentName:               "Coder",
		Mode:                    "CODER",
		PlanningMode:            false,
		ToolNames:               []string{"bash", "file_read", "file_write", "file_edit", "ask_user_question", "plan_add_tasks", "plan_get_tasks", "plan_update_task"},
		PlanningExcludeTools:    []string{"bash", "file_write", "file_edit", "plan_add_tasks", "plan_update_task"},
		PlanExecuteExcludeTools: []string{"ask_user_question"},
		ModeSystemPrompt:        "CODER {{agent_key}} {{agent_name}} {{planning_mode}} {{workspace_dir}} {{available_tools}} {{planning_stage_tools}} {{execute_stage_tools}} {{file_read_tool_name}} {{ask_user_question_tool_name}}",
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
			{Name: "plan_add_tasks"},
			{Name: "plan_get_tasks"},
			{Name: "plan_update_task"},
		},
	})

	for _, expected := range []string{
		"CODER coder Coder false /workspace",
		"bash, file_read, file_write, file_edit, ask_user_question, plan_add_tasks, plan_get_tasks, plan_update_task",
		"file_read, ask_user_question, plan_get_tasks, finalize_planning",
		"bash, file_read, file_write, file_edit, plan_add_tasks, plan_get_tasks, plan_update_task",
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

func TestBuildSystemPromptAddsKBaseSystemPromptOnlyForKBaseStage(t *testing.T) {
	session := QuerySession{
		AgentKey:         "docs",
		AgentName:        "Docs",
		Mode:             "KBASE",
		ToolNames:        []string{"kbase_search", "kbase_read", "datetime"},
		ModeSystemPrompt: "KBASE {{agent_key}} {{agent_name}} {{mode}} {{workspace_dir}} {{available_tools}}",
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{WorkspaceDir: "/docs"},
		},
	}
	main := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{
		Stage: "kbase",
		ToolDefinitions: []api.ToolDetailResponse{
			{Name: "kbase_search"},
			{Name: "kbase_read"},
			{Name: "datetime"},
		},
	})
	if !strings.Contains(main, "KBASE docs Docs KBASE /docs kbase_search, kbase_read, datetime") {
		t.Fatalf("expected rendered KBASE system prompt, got %q", main)
	}
	if strings.Contains(main, "{{") || strings.Contains(main, "}}") {
		t.Fatalf("expected KBASE system placeholders to be rendered, got %q", main)
	}
	otherStage := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{Stage: "coder"})
	if strings.Contains(otherStage, "KBASE docs Docs") {
		t.Fatalf("expected non-kbase stage to skip KBASE system prompt, got %q", otherStage)
	}
	react := buildSystemPrompt(QuerySession{
		AgentKey:         "docs",
		AgentName:        "Docs",
		Mode:             "REACT",
		ModeSystemPrompt: "KBASE {{agent_key}}",
	}, api.QueryRequest{}, "", PromptBuildOptions{Stage: "kbase"})
	if strings.Contains(react, "KBASE docs") {
		t.Fatalf("expected non-KBASE mode to skip KBASE system prompt, got %q", react)
	}
}

func TestBuildSystemPromptHasNoSourceKBasePromptWhenConfigEmpty(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:  "docs",
		AgentName: "Docs",
		Mode:      "KBASE",
		ToolNames: []string{"kbase_search", "kbase_read"},
	}, api.QueryRequest{}, "", PromptBuildOptions{Stage: "kbase"})

	for _, unexpected := range []string{"KBASE Mode", "Knowledge Base Capability", "KBASE File Workspace", "kbase_refresh"} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("did not expect source KBASE text %q, got %q", unexpected, prompt)
		}
	}
}

func TestBuildSystemPromptInjectsEmbeddedKBaseCapabilityOnce(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:          "zenmi",
		AgentName:         "Zenmi",
		Mode:              "REACT",
		KBaseEnabled:      true,
		CapabilityPrompts: []string{"Knowledge Base Capability\nconfigured rules"},
		ToolNames:         knowledge.DefaultToolNames(),
	}, api.QueryRequest{}, "", PromptBuildOptions{})
	if count := strings.Count(prompt, "Knowledge Base Capability"); count != 1 {
		t.Fatalf("capability prompt count = %d, want 1; prompt=%q", count, prompt)
	}
	if !strings.Contains(prompt, "configured rules") {
		t.Fatalf("configured capability rules missing: %q", prompt)
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

func TestBuildRuntimeContextPromptAutoIncludesSandboxSection(t *testing.T) {
	prompt := runtimeSystemPromptForTest(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			SandboxContext: &SandboxContext{
				EnvironmentID:     "browser",
				Level:             "RUN",
				EnvironmentPrompt: "Use the browser sandbox carefully.",
			},
		},
	})

	if !strings.Contains(prompt, "Runtime Context: Sandbox") {
		t.Fatalf("expected sandbox section in prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "environmentId: browser") {
		t.Fatalf("expected sandbox environment details in prompt, got %q", prompt)
	}
}

func TestBuildRuntimeContextPromptIncludesSelectedMemorySection(t *testing.T) {
	prompt := runtimeSystemPromptForTest(QuerySession{
		ContextTags:         []string{"memory-global"},
		GlobalMemoryContext: "Personal Memory\n- stable-fact",
	})

	if !strings.Contains(prompt, "Personal Memory") {
		t.Fatalf("expected memory section in prompt, got %q", prompt)
	}
}

func TestBuildRuntimeContextPromptIgnoresMemoryFromRequestParams(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{}, api.QueryRequest{
		Params: map[string]any{
			"memoryContext": "param-memory",
		},
	}, "", PromptBuildOptions{})

	if strings.Contains(prompt, "Runtime Context: Agent Memory") {
		t.Fatalf("expected request params not to inject memory, got %q", prompt)
	}
}

func TestBuildRuntimeContextPromptIgnoresDesktopParams(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{}, api.QueryRequest{
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
					"url":   "https://www.example.test/",
				},
			},
		},
	}, "", PromptBuildOptions{})

	for _, unexpected := range []string{
		"Runtime Context: Desktop",
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
		"currentPageUrl: https://www.example.test/",
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

			LocalMode: false,
			Scene:     &api.Scene{Title: "BrandApp", URL: "https://example.com"},
			AuthIdentity: &AuthIdentity{
				Subject:   "user-1",
				DeviceID:  "device-1",
				Scope:     "chat:write",
				IssuedAt:  "2026-04-23T09:00:00Z",
				ExpiresAt: "2026-04-23T10:00:00Z",
			},
			References: []api.Reference{
				{ID: "ref-1", Name: "doc.md", Path: "/workspace/doc.md"},
			},
			LocalPaths: LocalPaths{
				RootDir:         "/Users/tester/Project/app/runtime",
				PanDir:          "/Users/tester/Project/app/pan",
				AgentDir:        "/Users/tester/Project/app/runtime/agents/demo-agent",
				OwnerDir:        "/Users/tester/Project/app/runtime/owner",
				MemoryDir:       "/Users/tester/Project/app/runtime/memory",
				SkillsDir:       "/Users/tester/Project/app/runtime/agents/demo-agent/skills",
				SkillsCenterDir: "/Users/tester/Project/app/runtime/skills-center",
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
	})

	if !strings.Contains(section, "Runtime Context: Session") {
		t.Fatalf("expected session header, got %q", section)
	}
	if !strings.Contains(section, "chatId: chat-1") {
		t.Fatalf("expected chatId in session section, got %q", section)
	}
	if strings.Contains(section, "runId:") || strings.Contains(section, "requestId:") {
		t.Fatalf("expected session section to exclude volatile run identifiers, got %q", section)
	}
	if !strings.Contains(section, "scene: title=BrandApp, url=https://example.com") {
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
	if strings.Contains(section, "references:") || strings.Contains(section, "id: ref-1") || strings.Contains(section, "doc.md") {
		t.Fatalf("expected session section to exclude references, got %q", section)
	}
	if strings.Contains(section, "workspace_dir:") || strings.Contains(section, "chat_dir:") || strings.Contains(section, "agent_dir:") {
		t.Fatalf("expected session section to exclude path fields, got %q", section)
	}
	assertOrderedSubstrings(t, section, []string{
		"chatId:",
		"scene:",
		"subject:",
		"deviceId:",
		"scope:",
		"issuedAt:",
		"expiresAt:",
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
				WorkspaceDir:    "/Users/tester/Project/workspaces/demo",
				ChatDir:         "/Users/tester/Project/app/runtime/chats/chat-1",
				RootDir:         "/Users/tester/Project/app/runtime/root",
				SkillsDir:       "/Users/tester/Project/app/runtime/agents/demo-agent/skills",
				AgentDir:        "/Users/tester/Project/app/runtime/agents/demo-agent",
				OwnerDir:        "/Users/tester/Project/app/runtime/owner",
				SkillsCenterDir: "/Users/tester/Project/app/runtime/skills-center",
				AgentsDir:       "/Users/tester/Project/app/runtime/agents",
				RUAgentsDir:     "/Users/tester/Project/app/runtime/ru-agents",

				AutomationsDir:      "/Users/tester/Project/app/runtime/automations",
				ChatsDir:            "/Users/tester/Project/app/runtime/chats",
				MemoryDir:           "/Users/tester/Project/app/runtime/memory",
				ModelsDir:           "/Users/tester/Project/app/runtime/registries/models",
				ProvidersDir:        "/Users/tester/Project/app/runtime/registries/providers",
				ConnectorsCenterDir: "/Users/tester/Project/app/runtime/registries/mcp-servers",
				ToolsDir:            "/Users/tester/Project/app/runtime/tools",
				PanDir:              "/Users/tester/Server/pan",
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
	if !strings.Contains(section, "workspace_dir: /Users/tester/Project/workspaces/demo") {
		t.Fatalf("expected project workspace path in system environment, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /Users/tester/Project/app/runtime/chats/chat-1") {
		t.Fatalf("expected chat dir in system environment, got %q", section)
	}
	if strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected local paths instead of sandbox paths, got %q", section)
	}
	if !strings.Contains(section, "agents_dir: /Users/tester/Project/app/runtime/agents # Editable Agent source directory") {
		t.Fatalf("expected editable agents source in system environment, got %q", section)
	}
	if !strings.Contains(section, "ru_agents_dir: /Users/tester/Project/app/runtime/ru-agents # Platform-generated Agent execution directory; do not edit manually") {
		t.Fatalf("expected generated agent runtime root in system environment, got %q", section)
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
				WorkspaceDir: "/",
				ChatDir:      "/Users/tester/Project/app/runtime/chats/chat-1",
			},
		},
	})

	if !strings.Contains(section, "workspace_dir: / # Relative path base / permission workspace root") {
		t.Fatalf("expected explicit workspace root in system environment, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /Users/tester/Project/app/runtime/chats/chat-1") {
		t.Fatalf("expected separate chat dir in system environment, got %q", section)
	}
	if strings.Contains(section, "workspace_dir: /Users/tester/Project/app/agent-platform") {
		t.Fatalf("expected process cwd not to be used as workspace_dir, got %q", section)
	}
}

func TestBuildSystemEnvironmentSectionReportsUnavailableWorkspaceWithoutFallingBackToChat(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{
				ChatDir: "/Users/tester/Project/app/runtime/chats/chat-1",
			},
		},
	})

	if !strings.Contains(section, "workspace_dir: unavailable") {
		t.Fatalf("expected unavailable workspace to be explicit, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /Users/tester/Project/app/runtime/chats/chat-1") {
		t.Fatalf("expected independent chat dir, got %q", section)
	}
}

func TestBuildSystemEnvironmentSectionUsesSandboxPathsWhenSandboxEnabled(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			LocalPaths: LocalPaths{
				ChatDir:  "/Users/tester/Project/app/runtime/chats/chat-1",
				AgentDir: "/Users/tester/Project/app/runtime/agents/demo-agent",
			},
			SandboxPaths: SandboxPaths{
				WorkspaceDir:    "/workspace",
				ChatDir:         "/chat",
				RootDir:         "/root",
				SkillsDir:       "/skills",
				AgentDir:        "/agent",
				OwnerDir:        "/owner",
				SkillsCenterDir: "/skills-center",
				RUAgentsDir:     "/agents",

				AutomationsDir:      "/automations",
				ChatsDir:            "/chats",
				MemoryDir:           "/memory",
				ModelsDir:           "/models",
				ProvidersDir:        "/providers",
				ConnectorsCenterDir: "/mcp-servers",
				ToolsDir:            "/tools",
				PanDir:              "/pan",
			},
		},
	})

	if !strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected sandbox workspace dir in system environment, got %q", section)
	}
	if !strings.Contains(section, "chat_dir: /chat") {
		t.Fatalf("expected sandbox chat dir in system environment, got %q", section)
	}
	if strings.Contains(section, "/Users/tester/Project/app/agent-platform") {
		t.Fatalf("expected sandbox paths to win when sandbox is enabled, got %q", section)
	}
	if strings.Contains("\n"+section, "\nagents_dir:") {
		t.Fatalf("expected sandbox agents source to remain unavailable, got %q", section)
	}
	if !strings.Contains(section, "ru_agents_dir: /agents # Platform-generated Agent execution directory; do not edit manually") {
		t.Fatalf("expected existing /agents mount to be identified as generated runtime content, got %q", section)
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

func TestBuildSystemEnvironmentSectionOmitsSkillsCenterByDefault(t *testing.T) {
	localSection := buildSystemEnvironmentSection(QuerySession{
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{
				AgentDir:  "/agents/demo",
				SkillsDir: "/agents/demo/skills",
			},
		},
	})
	if strings.Contains(localSection, "skills_center_dir:") {
		t.Fatalf("expected local system environment to omit skills_center_dir, got %q", localSection)
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
	if strings.Contains(sandboxSection, "skills_center_dir:") {
		t.Fatalf("expected sandbox system environment to omit skills_center_dir, got %q", sandboxSection)
	}
}

func TestBuildSystemEnvironmentSectionIncludesExplicitSkillsCenter(t *testing.T) {
	localSection := buildSystemEnvironmentSection(QuerySession{
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{
				SkillsCenterDir: "/runtime/skills-center",
			},
		},
	})
	if !strings.Contains(localSection, "skills_center_dir: /runtime/skills-center") {
		t.Fatalf("expected explicit local skills_center_dir, got %q", localSection)
	}

	sandboxSection := buildSystemEnvironmentSection(QuerySession{
		AgentHasRuntimeSandbox: true,
		RuntimeContext: RuntimeRequestContext{
			SandboxPaths: SandboxPaths{
				WorkspaceDir:    "/workspace",
				SkillsCenterDir: "/skills-center",
			},
		},
	})
	if !strings.Contains(sandboxSection, "skills_center_dir: /skills-center") {
		t.Fatalf("expected explicit sandbox skills_center_dir, got %q", sandboxSection)
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

			LocalPaths: LocalPaths{
				WorkspaceDir: "/Users/tester/Project/workspaces/demo",
				ChatDir:      "/Users/tester/Project/app/runtime/chats/chat-1",
				AgentDir:     "/Users/tester/Project/app/runtime/agents/demo-agent",
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
	if !strings.Contains(prompt, "workspace_dir: /Users/tester/Project/workspaces/demo") {
		t.Fatalf("expected final prompt to include project workspace dir, got %q", prompt)
	}
	if !strings.Contains(prompt, "chat_dir: /Users/tester/Project/app/runtime/chats/chat-1") {
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

func TestBuildSystemPromptIgnoresRetiredAgentsTag(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:    "demo",
		ContextTags: []string{"agents"},
	}, api.QueryRequest{}, "", PromptBuildOptions{})

	if strings.Contains(prompt, "Runtime Context: Sub-Agent Candidates") {
		t.Fatalf("expected retired sub-agent candidates section to be omitted, got %q", prompt)
	}
}

func TestBuildToolAppendixIncludesOnlyAfterCallHints(t *testing.T) {
	appendix := buildToolAppendix([]api.ToolDetailResponse{
		{
			Name:          "z_tool",
			Description:   "z description",
			AfterCallHint: "z hint",
			Meta:          map[string]any{},
		},
		{
			Name:          "a_tool",
			Description:   "a description",
			AfterCallHint: "a hint",
			Meta:          map[string]any{"kind": "mcp"},
		},
	}, DefaultPromptAppendConfig(), true)

	if !strings.Contains(appendix, "After-call hints:") {
		t.Fatalf("expected after-call hint title, got %q", appendix)
	}
	if !strings.Contains(appendix, "- a_tool: a hint") || !strings.Contains(appendix, "- z_tool: z hint") {
		t.Fatalf("expected hint lines, got %q", appendix)
	}
	if strings.Contains(appendix, "工具说明:") || strings.Contains(appendix, "a description") || strings.Contains(appendix, "[interactions]") || strings.Contains(appendix, "[mcp]") {
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

func TestMemoryPromptFollowsTagSelectionAndOrder(t *testing.T) {
	for _, tags := range [][]string{nil, {"memory-global"}, {"memory-agent"}, {"memory-agent", "memory-global"}} {
		session := QuerySession{ContextTags: tags, GlobalMemoryContext: "GLOBAL_FACT", AgentMemoryContext: "AGENT_FACT"}
		prompt := runtimeSystemPromptForTest(session)
		global, agent := false, false
		for _, tag := range tags {
			global = global || tag == "memory-global"
			agent = agent || tag == "memory-agent"
		}
		if strings.Contains(prompt, "GLOBAL_FACT") != global || strings.Contains(prompt, "AGENT_FACT") != agent {
			t.Fatalf("tags=%v prompt=%s", tags, prompt)
		}
		if len(tags) == 2 && strings.Index(prompt, "AGENT_FACT") > strings.Index(prompt, "GLOBAL_FACT") {
			t.Fatal("tag order lost")
		}
	}
}

func TestSystemEnvironmentShowsRuntimeRelativeLocalPaths(t *testing.T) {
	home := filepath.Join(string(filepath.Separator)+"srv", "runtime")
	workspace := filepath.Join(string(filepath.Separator)+"srv", "project")
	lines := []string{}
	appendLocalContextPaths(&lines, LocalPaths{
		RuntimeHome:  home,
		WorkspaceDir: workspace,
		ChatDir:      filepath.Join(home, "chats", "chat-1"),
		ChatsDir:     filepath.Join(home, "chats"),
		PanDir:       filepath.Join(string(filepath.Separator)+"srv", "runtime-pan"),
	})
	got := strings.Join(lines, "\n")
	for _, expected := range []string{
		"runtime_dir: " + home + " #",
		"workspace_dir: " + workspace + " #",
		"chat_dir: @runtime/chats/chat-1 #",
		"chats_dir: @runtime/chats #",
		"pan_dir: " + filepath.Join(string(filepath.Separator)+"srv", "runtime-pan") + " #",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in:\n%s", expected, got)
		}
	}

	lines = lines[:0]
	chatDir := filepath.Join(home, "chats", "chat-1")
	appendLocalContextPaths(&lines, LocalPaths{ChatDir: chatDir})
	if got := strings.Join(lines, "\n"); strings.Contains(got, "@runtime") || !strings.Contains(got, "chat_dir: "+chatDir) {
		t.Fatalf("expected absolute paths without a runtime root, got:\n%s", got)
	}
}
