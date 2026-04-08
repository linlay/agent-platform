package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
)

func TestBuildSystemPromptMatchesJavaLayerOrder(t *testing.T) {
	ownerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ownerDir, "OWNER.md"), []byte("# Owner\n\ncontent"), 0o644); err != nil {
		t.Fatalf("write owner file: %v", err)
	}
	size := int64(42)
	prompt := buildSystemPrompt(QuerySession{
		RequestID:          "req_1",
		RunID:              "run_1",
		ChatID:             "chat_1",
		AgentKey:           "agent_1",
		TeamID:             "team_1",
		SoulPrompt:         "soul prompt",
		AgentsPrompt:       "plain markdown",
		MemoryPrompt:       "memory markdown",
		SkillCatalogPrompt: "可用 skills（目录摘要，按需使用，不要虚构不存在的 skill 或脚本）:\n\nskillId: mock-skill",
		ContextTags:        []string{"context", "owner", "auth", "memory"},
		RuntimeContext: RuntimeRequestContext{
			AgentKey: "agent_1",
			TeamID:   "team_1",
			References: []api.Reference{
				{ID: "ref_1", SandboxPath: "/workspace/a.md", Name: "a.md", MimeType: "text/markdown", SizeBytes: &size},
			},
			AuthIdentity: &AuthIdentity{Subject: "user-1"},
			LocalPaths:   LocalPaths{OwnerDir: ownerDir},
			SandboxPaths: SandboxPaths{WorkspaceDir: "/workspace"},
		},
		MemoryContext: "id: mem_1\ncontent: cached memory",
		ResolvedStageSettings: PlanExecuteSettings{
			Execute: StageSettings{SystemPrompt: "yaml prompt"},
		},
		PromptAppend: DefaultPromptAppendConfig(),
	}, api.QueryRequest{}, "mock-model", PromptBuildOptions{
		Stage: "execute",
		ToolDefinitions: []api.ToolDetailResponse{
			{Key: "_datetime_", Name: "_datetime_", Description: "时间工具", AfterCallHint: "继续汇报结果", Meta: map[string]any{"kind": "backend"}},
		},
		IncludeAfterCallHints: true,
	})

	assertOrdered(t, prompt,
		"soul prompt",
		"Runtime Context: Context",
		"plain markdown",
		"memory markdown",
		"yaml prompt",
		"可用 skills（目录摘要，按需使用，不要虚构不存在的 skill 或脚本）:",
		"工具说明:",
	)
	if !strings.Contains(prompt, "Runtime Context: Owner") {
		t.Fatalf("expected owner section, got %q", prompt)
	}
	if !strings.Contains(prompt, "Runtime Context: Auth Identity") {
		t.Fatalf("expected auth section, got %q", prompt)
	}
	if !strings.Contains(prompt, "Runtime Context: Agent Memory") {
		t.Fatalf("expected memory section, got %q", prompt)
	}
	if !strings.Contains(prompt, "references:\n  - id: ref_1") {
		t.Fatalf("expected structured references, got %q", prompt)
	}
}

func TestBuildSystemPromptUsesStageSpecificPlanExecutePrompts(t *testing.T) {
	session := QuerySession{
		SoulPrompt:    "soul prompt",
		PlanPrompt:    "plan markdown",
		ExecutePrompt: "execute markdown",
		SummaryPrompt: "summary markdown",
		ResolvedStageSettings: PlanExecuteSettings{
			Plan:    StageSettings{SystemPrompt: "plan yaml"},
			Execute: StageSettings{SystemPrompt: "execute yaml"},
			Summary: StageSettings{SystemPrompt: "summary yaml"},
		},
	}

	planPrompt := buildSystemPrompt(session, api.QueryRequest{}, "mock-model", PromptBuildOptions{Stage: "plan"})
	if strings.Contains(planPrompt, "execute markdown") || strings.Contains(planPrompt, "summary markdown") {
		t.Fatalf("expected plan stage prompt only, got %q", planPrompt)
	}
	if !strings.Contains(planPrompt, "plan markdown") || !strings.Contains(planPrompt, "plan yaml") {
		t.Fatalf("expected plan prompts, got %q", planPrompt)
	}

	summaryPrompt := buildSystemPrompt(session, api.QueryRequest{}, "mock-model", PromptBuildOptions{Stage: "summary"})
	if !strings.Contains(summaryPrompt, "summary markdown") || !strings.Contains(summaryPrompt, "summary yaml") {
		t.Fatalf("expected summary prompts, got %q", summaryPrompt)
	}
}

func TestBuildSystemPromptUsesCustomAppendixTitles(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		SkillCatalogPrompt: "skills body",
		PromptAppend: PromptAppendConfig{
			Tool: ToolAppendConfig{
				ToolDescriptionTitle: "tool-desc-title-override",
				AfterCallHintTitle:   "tool-hint-title-override",
			},
		},
	}, api.QueryRequest{}, "mock-model", PromptBuildOptions{
		ToolDefinitions: []api.ToolDetailResponse{
			{Key: "_datetime_", Name: "_datetime_", Description: "时间工具", AfterCallHint: "继续执行", Meta: map[string]any{"kind": "backend"}},
		},
		IncludeAfterCallHints: true,
	})

	if !strings.Contains(prompt, "tool-desc-title-override") {
		t.Fatalf("expected custom tool description title, got %q", prompt)
	}
	if !strings.Contains(prompt, "tool-hint-title-override") {
		t.Fatalf("expected custom tool hint title, got %q", prompt)
	}
}

func TestBuildSystemPromptContextTagShowsOwnerAndMemoryPathsWithoutInjectingSections(t *testing.T) {
	prompt := buildSystemPrompt(QuerySession{
		RequestID:    "req_1",
		RunID:        "run_1",
		ChatID:       "chat_1",
		AgentKey:     "agent_1",
		ContextTags:  []string{"context"},
		MemoryPrompt: "memory markdown",
		RuntimeContext: RuntimeRequestContext{
			AgentKey: "agent_1",
			SandboxPaths: SandboxPaths{
				WorkspaceDir: "/workspace",
				OwnerDir:     "/owner",
				MemoryDir:    "/memory",
			},
			LocalPaths: LocalPaths{
				OwnerDir: "/tmp/owner",
			},
		},
		ResolvedStageSettings: PlanExecuteSettings{
			Execute: StageSettings{SystemPrompt: "yaml prompt"},
		},
	}, api.QueryRequest{}, "mock-model", PromptBuildOptions{
		Stage: "execute",
	})

	if !strings.Contains(prompt, "sandbox_owner_dir: /owner") {
		t.Fatalf("expected sandbox owner dir in context section, got %q", prompt)
	}
	if !strings.Contains(prompt, "sandbox_memory_dir: /memory") {
		t.Fatalf("expected sandbox memory dir in context section, got %q", prompt)
	}
	if strings.Contains(prompt, "Runtime Context: Owner") {
		t.Fatalf("did not expect owner section for context-only tags, got %q", prompt)
	}
	if strings.Contains(prompt, "Runtime Context: Agent Memory") {
		t.Fatalf("did not expect memory section for context-only tags, got %q", prompt)
	}
}

func assertOrdered(t *testing.T, text string, parts ...string) {
	t.Helper()
	last := -1
	for _, part := range parts {
		index := strings.Index(text, part)
		if index < 0 {
			t.Fatalf("expected %q in %q", part, text)
		}
		if index <= last {
			t.Fatalf("expected %q after previous section in %q", part, text)
		}
		last = index
	}
}
