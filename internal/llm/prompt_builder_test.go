package llm

import (
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
)

func TestBuildSystemPromptPlacesStaticMemoryBeforeRuntimeMemory(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
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
				WorkingDirectory: "/Users/linlay/Project/zenmind/agent-platform",
				RootDir:          "/Users/linlay/Project/zenmind/zenmind-env/root",
				AgentDir:         "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
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
	if !strings.Contains(section, "workspace_dir: /Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected local workspace path in system environment, got %q", section)
	}
	if strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected local paths instead of sandbox paths, got %q", section)
	}
	if strings.Contains(section, "chatId:") || strings.Contains(section, "runId:") || strings.Contains(section, "requestId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", section)
	}
}

func TestBuildSystemEnvironmentSectionUsesSandboxPathsWhenSandboxEnabled(t *testing.T) {
	section := buildSystemEnvironmentSection(QuerySession{
		AgentHasSandboxConfig: true,
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
			LocalPaths: LocalPaths{
				WorkingDirectory: "/Users/linlay/Project/zenmind/agent-platform",
				AgentDir:         "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
			},
			SandboxPaths: SandboxPaths{
				WorkspaceDir: "/workspace",
				RootDir:      "/root",
				PanDir:       "/pan",
				AgentDir:     "/agent",
				OwnerDir:     "/owner",
				MemoryDir:    "/memory",
			},
		},
	})

	if !strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected sandbox workspace dir in system environment, got %q", section)
	}
	if strings.Contains(section, "/Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected sandbox paths to win when sandbox is enabled, got %q", section)
	}
	if strings.Contains(section, "chatId:") {
		t.Fatalf("expected system environment to exclude session identifiers, got %q", section)
	}
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
				WorkingDirectory: "/Users/linlay/Project/zenmind/agent-platform",
				AgentDir:         "/Users/linlay/Project/zenmind/zenmind-env/agents/zenmi",
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
	if !strings.Contains(prompt, "workspace_dir: /Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected final prompt to include local workspace dir, got %q", prompt)
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
}
