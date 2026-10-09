package kbase

import (
	"reflect"
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

// Every KBASE prompt part comes from agent-prompt.yml; the mode only orders and
// renders the configured parts.
func kbasePromptPartsSession() contracts.QuerySession {
	return contracts.QuerySession{
		Mode:             Mode,
		WorkspaceRoot:    "/knowledge",
		ToolNames:        CreateToolNames(),
		ModeSystemPrompt: "SYSTEM {{mode}}",
		KBaseModePrompts: contracts.KBaseModePrompts{
			Capability: "CAPABILITY",
			Workspace:  "WORKSPACE {{workspace_dir}} {{chat_dir}}",
			Editing:    "EDITING",
		},
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: "/knowledge", ChatDir: "/runtime/chats/chat-1"},
		},
	}
}

func TestEditingPromptAppendsConfiguredPartsInOrder(t *testing.T) {
	session := kbasePromptPartsSession()
	session.EditingMode = true
	prompt := RenderSystemPrompt(session, api.QueryRequest{Message: "update policy"}, CreateToolNames(), EditingStage)
	want := "CAPABILITY\n\nSYSTEM KBASE\n\nWORKSPACE /knowledge /runtime/chats/chat-1\n\nEDITING"
	if prompt != want {
		t.Fatalf("editing prompt = %q, want %q", prompt, want)
	}
}

func TestMainPromptOmitsEditingPartAndHasNoSourceText(t *testing.T) {
	session := kbasePromptPartsSession()
	prompt := RenderSystemPrompt(session, api.QueryRequest{Message: "write a report"}, CreateToolNames(), MainStage)
	if want := "CAPABILITY\n\nSYSTEM KBASE\n\nWORKSPACE /knowledge /runtime/chats/chat-1"; prompt != want {
		t.Fatalf("main prompt = %q, want %q", prompt, want)
	}
	session.ModeSystemPrompt = ""
	session.KBaseModePrompts = contracts.KBaseModePrompts{}
	if prompt := RenderSystemPrompt(session, api.QueryRequest{}, CreateToolNames(), MainStage); prompt != "" {
		t.Fatalf("expected no source KBASE prompt without configuration, got %q", prompt)
	}
}
