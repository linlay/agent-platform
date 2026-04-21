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
