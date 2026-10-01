package kbase

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestKBaseHasNoFixedToolBoundary(t *testing.T) {
	if tools := Descriptor().Profile.ToolNames; len(tools) != 0 {
		t.Fatalf("KBASE must not supply load-time tool defaults: %#v", tools)
	}
	if got := EditingSystemInitSpec().ToolNames; len(got) != 0 {
		t.Fatalf("editing stage must use the agent's declared tools: %#v", got)
	}
	want := []string{ToolDatetime, "file_read", "file_glob", "file_grep", "file_write", "file_edit"}
	if !reflect.DeepEqual(CreateToolNames(), want) {
		t.Fatalf("creation tool list = %#v, want %#v", CreateToolNames(), want)
	}
}

func TestApplyCreateToolDefaultsOnlyWhenToolsAreNotDeclared(t *testing.T) {
	created := ApplyCreateToolDefaults(map[string]any{"mode": Mode})
	tools, _ := created["toolConfig"].(map[string]any)["tools"].([]any)
	if len(tools) != len(CreateToolNames()) || tools[1] != "file_read" {
		t.Fatalf("creation tools not written: %#v", created["toolConfig"])
	}
	for name, explicit := range map[string][]any{"empty": {}, "custom": {"bash"}} {
		kept := ApplyCreateToolDefaults(map[string]any{"toolConfig": map[string]any{"tools": explicit}})
		if got := kept["toolConfig"].(map[string]any)["tools"].([]any); len(got) != len(explicit) {
			t.Fatalf("%s: explicit tool list was replaced: %#v", name, got)
		}
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
}

func TestEditingPromptUsesAccessPolicyAndAsynchronousIndexing(t *testing.T) {
	prompt := RenderSystemPrompt(contracts.QuerySession{
		Mode:          Mode,
		EditingMode:   true,
		WorkspaceRoot: "/knowledge",
		ToolNames:     CreateToolNames(),
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: "/knowledge", ChatDir: "/runtime/chats/chat-1"},
		},
	}, api.QueryRequest{Message: "update policy"}, CreateToolNames(), EditingStage)
	for _, want := range []string{
		"/knowledge",
		"/runtime/chats/chat-1",
		"file_edit",
		"AccessPolicy",
		"configured Workspace is the knowledge root",
		"explicit current chat directory path",
		"directory watcher",
		"does not mean the change is immediately searchable",
		"lineStats",
		"Do not use shell commands or other tools to change the Workspace while editingMode is off",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("editing prompt missing %q: %s", want, prompt)
		}
	}
}

func TestMainPromptDefinesSourceWorkspaceAndWritableChatDirectory(t *testing.T) {
	prompt := RenderSystemPrompt(contracts.QuerySession{
		Mode:          Mode,
		WorkspaceRoot: "/knowledge",
		ToolNames:     CreateToolNames(),
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{
				WorkspaceDir: "/knowledge",
				ChatDir:      "/runtime/chats/chat-1",
			},
		},
	}, api.QueryRequest{Message: "write a report"}, CreateToolNames(), MainStage)
	for _, want := range []string{
		"/knowledge",
		"/runtime/chats/chat-1",
		"Relative file-tool paths resolve inside this workspace",
		"Use only the tools declared for this agent",
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
