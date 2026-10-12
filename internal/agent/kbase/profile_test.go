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
	if got := MainSystemInitSpec().ToolNames; len(got) != 0 {
		t.Fatalf("the main stage must use the agent's declared tools: %#v", got)
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

// Every KBASE prompt part comes from agent-prompt.yml; the mode only orders and
// renders the configured parts.
func kbasePromptPartsSession() contracts.QuerySession {
	return contracts.QuerySession{
		Mode:             Mode,
		WorkspaceRoot:    "/knowledge",
		ToolNames:        CreateToolNames(),
		ModeSystemPrompt: "SYSTEM {{mode}}",
		KBaseModePrompts: contracts.KBaseModePrompts{
			Workspace: "WORKSPACE {{workspace_dir}} {{chat_dir}}",
		},
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{WorkspaceDir: "/knowledge", ChatDir: "/runtime/chats/chat-1"},
		},
	}
}

func TestPromptDoesNotDependOnEditingMode(t *testing.T) {
	session := kbasePromptPartsSession()
	readOnly := RenderSystemPrompt(session, api.QueryRequest{Message: "update policy"}, CreateToolNames(), MainStage)
	session.EditingMode = true
	editing := RenderSystemPrompt(session, api.QueryRequest{Message: "update policy"}, CreateToolNames(), MainStage)
	if readOnly != editing || readOnly != "SYSTEM KBASE\n\nWORKSPACE /knowledge /runtime/chats/chat-1" {
		t.Fatalf("editingMode changed the prompt: %q vs %q", readOnly, editing)
	}
}

func TestMainPromptHasNoSourceText(t *testing.T) {
	session := kbasePromptPartsSession()
	prompt := RenderSystemPrompt(session, api.QueryRequest{Message: "write a report"}, CreateToolNames(), MainStage)
	if want := "SYSTEM KBASE\n\nWORKSPACE /knowledge /runtime/chats/chat-1"; prompt != want {
		t.Fatalf("main prompt = %q, want %q", prompt, want)
	}
	session.ModeSystemPrompt = ""
	session.KBaseModePrompts = contracts.KBaseModePrompts{}
	if prompt := RenderSystemPrompt(session, api.QueryRequest{}, CreateToolNames(), MainStage); prompt != "" {
		t.Fatalf("expected no source KBASE prompt without configuration, got %q", prompt)
	}
}
