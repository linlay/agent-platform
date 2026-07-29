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
	if !reflect.DeepEqual(policy.ToolNames, DefaultToolNames()) {
		t.Fatalf("dedicated KBASE tools = %#v, want %#v", policy.ToolNames, DefaultToolNames())
	}

	defaults := ResolveBoundaryPolicy([]string{"bash", "memory_search"})
	if !reflect.DeepEqual(defaults.ToolNames, DefaultToolNames()) {
		t.Fatalf("invalid-only tools must fall back to KBASE defaults: %#v", defaults.ToolNames)
	}
}

func TestEditingProfileUsesIndependentStageCacheAndExactTools(t *testing.T) {
	if Descriptor().Capabilities.FileChangeHooks {
		t.Fatal("KBASE mode must not enable synchronous file-change hooks")
	}
	if RuntimeStage(true) != EditingStage || SystemInitCacheKey(EditingStage) != EditingCacheKey {
		t.Fatalf("unexpected editing stage/cache: %q %q", RuntimeStage(true), SystemInitCacheKey(EditingStage))
	}
	spec := EditingSystemInitSpec()
	if spec.CacheKey != EditingCacheKey || spec.FingerprintStage != EditingStage ||
		spec.PromptStage != EditingStage || spec.Mode != MainStage || spec.Stage != "editing" {
		t.Fatalf("unexpected editing system-init spec: %#v", spec)
	}
	want := DefaultToolNames()
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

func TestEditingPromptUsesAccessPolicyAndAsynchronousIndexing(t *testing.T) {
	prompt := RenderSystemPrompt(contracts.QuerySession{
		Mode:            Mode,
		EditingMode:     true,
		KBaseSourceRoot: "/knowledge",
		ToolNames:       EditingToolNames(),
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: "/runtime/chats/chat-1"},
		},
	}, api.QueryRequest{Message: "update policy"}, EditingToolNames(), EditingStage)
	for _, want := range []string{
		"/knowledge",
		"/runtime/chats/chat-1",
		"file_edit",
		"AccessPolicy",
		"knowledge source is the workspace",
		"explicit current chat directory path",
		"directory watcher",
		"does not mean the change is immediately searchable",
		"lineStats",
		"Do not use shell commands",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("editing prompt missing %q: %s", want, prompt)
		}
	}
}

func TestMainPromptDefinesSourceWorkspaceAndWritableChatDirectory(t *testing.T) {
	prompt := RenderSystemPrompt(contracts.QuerySession{
		Mode:            Mode,
		KBaseSourceRoot: "/knowledge",
		ToolNames:       DefaultToolNames(),
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir:       "/knowledge",
				ChatAttachmentsDir: "/runtime/chats/chat-1",
			},
		},
	}, api.QueryRequest{Message: "write a report"}, DefaultToolNames(), MainStage)
	for _, want := range []string{
		"/knowledge",
		"/runtime/chats/chat-1",
		"Relative file-tool paths resolve inside this workspace",
		"structured file tools are always available",
		"read-only unless this run explicitly enables editingMode",
		"Store conversation artifacts and temporary files under the explicit current chat directory path",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("main prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "The user explicitly enabled knowledge-source mutation") {
		t.Fatalf("main prompt must not claim source mutation is enabled: %s", prompt)
	}
}
