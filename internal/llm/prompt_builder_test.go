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

func TestBuildContextSectionUsesLocalPathsWithoutSandbox(t *testing.T) {
	section := buildContextSection(QuerySession{
		ChatID:    "chat-1",
		RunID:     "run-1",
		RequestID: "req-1",
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
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

	if !strings.Contains(section, "workspace_dir: /Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected local workspace dir in context, got %q", section)
	}
	if strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected local paths instead of sandbox paths, got %q", section)
	}
	if strings.Contains(section, "agent_dir: /agent") {
		t.Fatalf("expected local agent dir instead of sandbox agent dir, got %q", section)
	}
}

func TestBuildContextSectionUsesSandboxPathsWhenSandboxEnabled(t *testing.T) {
	section := buildContextSection(QuerySession{
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
	}, api.QueryRequest{})

	if !strings.Contains(section, "workspace_dir: /workspace") {
		t.Fatalf("expected sandbox workspace dir in context, got %q", section)
	}
	if strings.Contains(section, "/Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected sandbox paths to win when sandbox is enabled, got %q", section)
	}
}

func TestBuildSystemPromptUsesLocalContextPathsWithoutSandbox(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		ContextTags: []string{"context"},
		RuntimeContext: RuntimeRequestContext{
			LocalMode: false,
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

	if !strings.Contains(prompt, "workspace_dir: /Users/linlay/Project/zenmind/agent-platform") {
		t.Fatalf("expected final prompt to include local workspace dir, got %q", prompt)
	}
	if strings.Contains(prompt, "workspace_dir: /workspace") {
		t.Fatalf("expected final prompt to avoid sandbox workspace dir without sandbox config, got %q", prompt)
	}
}
