package kbase

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestResolveBoundaryPolicyOwnsToolsAndMemoryBoundary(t *testing.T) {
	policy := ResolveBoundaryPolicy([]string{"bash", ToolSearch, "memory_search"})
	if policy.MemoryEnabled {
		t.Fatal("KBASE boundary must disable memory")
	}
	if !reflect.DeepEqual(policy.ToolNames, []string{ToolSearch}) {
		t.Fatalf("filtered tools = %#v, want [%s]", policy.ToolNames, ToolSearch)
	}

	defaults := ResolveBoundaryPolicy([]string{"bash", "memory_search"})
	if !reflect.DeepEqual(defaults.ToolNames, DefaultToolNames()) {
		t.Fatalf("invalid-only tools must fall back to KBASE defaults: %#v", defaults.ToolNames)
	}
}

func TestEditingProfileUsesIndependentStageCacheAndExactTools(t *testing.T) {
	if RuntimeStage(true) != EditingStage || SystemInitCacheKey(EditingStage) != EditingCacheKey {
		t.Fatalf("unexpected editing stage/cache: %q %q", RuntimeStage(true), SystemInitCacheKey(EditingStage))
	}
	spec := EditingSystemInitSpec()
	if spec.CacheKey != EditingCacheKey || spec.FingerprintStage != EditingStage ||
		spec.PromptStage != EditingStage || spec.Mode != MainStage || spec.Stage != "editing" {
		t.Fatalf("unexpected editing system-init spec: %#v", spec)
	}
	want := append(DefaultToolNames(), "file_read", "file_glob", "file_grep", "file_write", "file_edit")
	if !reflect.DeepEqual(EditingToolNames(), want) {
		t.Fatalf("editing tools = %#v, want %#v", EditingToolNames(), want)
	}
	for _, forbidden := range []string{"bash", "file_delete", "file_move", "mkdir"} {
		for _, toolName := range EditingToolNames() {
			if toolName == forbidden {
				t.Fatalf("editing tools must not contain %q: %#v", forbidden, EditingToolNames())
			}
		}
	}
}

func TestEditingPromptRequiresExplicitScopedMutationAndIndexResult(t *testing.T) {
	prompt := RenderSystemPrompt(contracts.QuerySession{
		Mode:            Mode,
		EditingMode:     true,
		KBaseSourceRoot: "/knowledge",
		ToolNames:       EditingToolNames(),
	}, api.QueryRequest{Message: "update policy"}, EditingToolNames(), EditingStage)
	for _, want := range []string{"/knowledge", "file_edit", "kbase-index", "lineStats", "Do not use shell commands"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("editing prompt missing %q: %s", want, prompt)
		}
	}
}
