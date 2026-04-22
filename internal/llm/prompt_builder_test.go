package llm

import (
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
)

func TestBuildSystemPromptPlacesStaticMemoryBeforeRuntimeMemory(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		AgentKey:            "demo",
		AgentName:           "Demo",
		AgentRole:           "Prompt Tester",
		AgentDescription:    "Verifies prompt ordering",
		Mode:                "REACT",
		SoulPrompt:          "soul",
		StaticMemoryPrompt:  "static-memory",
		ContextTags:         []string{"memory"},
		StableMemoryContext: "Runtime Context: Stable Memory\n- stable-fact",
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
		AgentHasSandboxConfig: true,
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

func TestBuildSessionContextSectionUsesSessionFieldsWithoutPaths(t *testing.T) {
	section := buildSessionContextSection(QuerySession{
		ChatID:    "chat-1",
		RunID:     "run-1",
		RequestID: "req-1",
		RuntimeContext: RuntimeRequestContext{
			TeamID:    "team-1",
			LocalMode: false,
			Scene:     &api.Scene{Title: "ZenMind", URL: "https://example.com"},
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

	if !strings.Contains(section, "Runtime Context: Session Context") {
		t.Fatalf("expected session context header, got %q", section)
	}
	if !strings.Contains(section, "chatId: chat-1") || !strings.Contains(section, "runId: run-1") || !strings.Contains(section, "requestId: req-1") {
		t.Fatalf("expected session identifiers in session context, got %q", section)
	}
	if !strings.Contains(section, "teamId: team-1") || !strings.Contains(section, "scene: title=ZenMind, url=https://example.com") {
		t.Fatalf("expected team and scene in session context, got %q", section)
	}
	if !strings.Contains(section, "references:") || !strings.Contains(section, "id: ref-1") {
		t.Fatalf("expected references in session context, got %q", section)
	}
	if strings.Contains(section, "workspace_dir:") || strings.Contains(section, "agent_dir:") {
		t.Fatalf("expected session context to exclude path fields, got %q", section)
	}
}

func TestBuildSystemEnvironmentSectionUsesLocalPathsWithoutSandbox(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		ChatID:    "chat-1",
		RunID:     "run-1",
		RequestID: "req-1",
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			LocalPaths: LocalPaths{
				WorkingDirectory:   "/Users/linlay/Project/zenmind/agent-platform",
				ChatAttachmentsDir: "/Users/linlay/Project/zenmind/zenmind-env/chats/chat-1",
				RootDir:            "/Users/linlay/Project/zenmind/zenmind-env/root",
				SkillsDir:          "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi/skills",
				AgentDir:           "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
				OwnerDir:           "/Users/linlay/Project/zenmind/zenmind-env/owner",
				SkillsMarketDir:    "/Users/linlay/Project/zenmind/zenmind-env/skills-market",
				AgentsDir:          "/Users/linlay/Project/zenmind/zenmind-env/agents",
				TeamsDir:           "/Users/linlay/Project/zenmind/zenmind-env/teams",
				SchedulesDir:       "/Users/linlay/Project/zenmind/zenmind-env/schedules",
				ChatsDir:           "/Users/linlay/Project/zenmind/zenmind-env/chats",
				MemoryDir:          "/Users/linlay/Project/zenmind/zenmind-env/memory",
				ModelsDir:          "/Users/linlay/Project/zenmind/zenmind-env/registries/models",
				ProvidersDir:       "/Users/linlay/Project/zenmind/zenmind-env/registries/providers",
				MCPServersDir:      "/Users/linlay/Project/zenmind/zenmind-env/registries/mcp-servers",
				ViewportServersDir: "/Users/linlay/Project/zenmind/zenmind-env/registries/viewport-servers",
				ToolsDir:           "/Users/linlay/Project/zenmind/zenmind-env/registries/tools",
				ViewportsDir:       "/Users/linlay/Project/zenmind/zenmind-env/registries/viewports",
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
	if strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected local paths instead of sandbox paths, got %q", section)
	}
	if strings.Contains(section, "tools_dir:") || strings.Contains(section, "viewports_dir:") {
		t.Fatalf("expected tools_dir and viewports_dir to be removed, got %q", section)
	}
	if strings.Contains(section, "chatId:") || strings.Contains(section, "runId:") || strings.Contains(section, "requestId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", section)
	}
	assertOrderedSubstrings(t, section, []string{
		"workspace_dir:",
		"root_dir:",
		"skills_dir:",
		"agent_dir:",
		"owner_dir:",
	})
	assertLastField(t, section, "pan_dir:")
}

func TestBuildSystemEnvironmentSectionUsesSandboxPathsWhenSandboxEnabled(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		AgentHasSandboxConfig: true,
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
				SchedulesDir:       "/schedules",
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
	if strings.Contains(section, "/Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected sandbox paths to win when sandbox is enabled, got %q", section)
	}
	if strings.Contains(section, "tools_dir:") || strings.Contains(section, "viewports_dir:") {
		t.Fatalf("expected tools_dir and viewports_dir to be removed, got %q", section)
	}
	if strings.Contains(section, "chatId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", section)
	}
	assertOrderedSubstrings(t, section, []string{
		"workspace_dir:",
		"root_dir:",
		"skills_dir:",
		"agent_dir:",
		"owner_dir:",
	})
	assertLastField(t, section, "pan_dir:")
}

func TestBuildSystemPromptSeparatesSystemEnvironmentAndSessionContext(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		ChatID:      "chat-1",
		RunID:       "run-1",
		RequestID:   "req-1",
		ContextTags: []string{"system", "context"},
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			TeamID:    "team-1",
			LocalPaths: LocalPaths{
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
	sessionIndex := strings.Index(prompt, "Runtime Context: Session Context")
	if systemIndex < 0 || sessionIndex < 0 {
		t.Fatalf("expected both system environment and session context sections, got %q", prompt)
	}
	if strings.Contains(prompt, "Runtime Context: Context") {
		t.Fatalf("expected old context header to be removed, got %q", prompt)
	}
	if !strings.Contains(prompt, "workspace_dir: /Users/linlay/Project/zenmind/zenmind-env/chats/chat-1") {
		t.Fatalf("expected final prompt to include chat workspace dir, got %q", prompt)
	}
	if !strings.Contains(prompt, "chatId: chat-1") || !strings.Contains(prompt, "runId: run-1") || !strings.Contains(prompt, "requestId: req-1") {
		t.Fatalf("expected final prompt to include session identifiers, got %q", prompt)
	}
	sessionSection := prompt[sessionIndex:]
	if strings.Contains(sessionSection, "workspace_dir:") {
		t.Fatalf("expected session context to exclude workspace paths, got %q", sessionSection)
	}
	systemSection := prompt[systemIndex:sessionIndex]
	if strings.Contains(systemSection, "chatId:") || strings.Contains(systemSection, "runId:") || strings.Contains(systemSection, "requestId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", systemSection)
	}
	if strings.Contains(systemSection, "tools_dir:") || strings.Contains(systemSection, "viewports_dir:") {
		t.Fatalf("expected system environment to exclude tools_dir and viewports_dir, got %q", systemSection)
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
