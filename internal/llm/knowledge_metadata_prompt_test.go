package llm

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestBuildSystemPromptIncludesKnowledgeLibraryMetadataOnce(t *testing.T) {
	const metadata = "Knowledge library context:\n{\"boundLibraryId\":\"docs\",\"requiredLibraries\":[]}"
	prompt := buildSystemPrompt(contracts.QuerySession{
		AgentKey:              "zenmi",
		AgentName:             "Zenmi",
		Mode:                  "GENERAL",
		KBaseBindingLibraryID: "docs",
		CapabilityPrompts:     []string{metadata},
		ToolNames:             []string{"kbase_list", "kbase_search"},
	}, api.QueryRequest{}, "", PromptBuildOptions{})
	if count := strings.Count(prompt, metadata); count != 1 {
		t.Fatalf("library metadata count = %d, want 1; prompt=%q", count, prompt)
	}
	for _, retiredRule := range []string{"Knowledge Base Capability", "Use each required library", "Other libraries may be discovered"} {
		if strings.Contains(prompt, retiredRule) {
			t.Fatalf("library metadata injected retrieval rules: %q", prompt)
		}
	}
}
