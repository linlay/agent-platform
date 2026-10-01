package general

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsModeAcceptsLegacySpelling(t *testing.T) {
	if !IsMode("general") || !IsMode(" REACT ") || IsMode("CODER") || IsMode("") {
		t.Fatalf("unexpected IsMode result")
	}
}

func TestApplyCreateDefaultsFillsOnlyMissingValues(t *testing.T) {
	defaults := CreateDefaults{ModelKey: "default-model", ReasoningEffort: "HIGH", Budget: map[string]any{"maxSteps": 200}}
	filled := ApplyCreateDefaults(map[string]any{"mode": Mode}, defaults)
	modelConfig := filled["modelConfig"].(map[string]any)
	if modelConfig["modelKey"] != "default-model" || modelConfig["reasoning"].(map[string]any)["effort"] != "HIGH" {
		t.Fatalf("defaults not applied: %#v", modelConfig)
	}
	if filled["budget"].(map[string]any)["maxSteps"] != 200 {
		t.Fatalf("budget default not applied: %#v", filled["budget"])
	}
	if _, exists := filled["icon"]; exists {
		t.Fatalf("general agents have no type-owned icon: %#v", filled)
	}

	explicit := ApplyCreateDefaults(map[string]any{
		"modelConfig": map[string]any{"modelKey": "chosen"},
		"budget":      map[string]any{"maxSteps": 10},
	}, defaults)
	if explicit["modelConfig"].(map[string]any)["modelKey"] != "chosen" || explicit["budget"].(map[string]any)["maxSteps"] != 10 {
		t.Fatalf("explicit values were overwritten: %#v", explicit)
	}
	if empty := ApplyCreateDefaults(map[string]any{"mode": Mode}, CreateDefaults{}); len(empty) != 1 {
		t.Fatalf("empty defaults must not synthesize fields: %#v", empty)
	}
}

func TestLoadWorkspacePromptIsOptIn(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("  project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	load := func(policy WorkspacePromptPolicy) string {
		t.Helper()
		prompt, err := LoadWorkspacePrompt(policy)
		if err != nil {
			t.Fatal(err)
		}
		return prompt
	}
	enabled := WorkspacePromptPolicy{Mode: Mode, WorkspaceRoot: workspace, WorkspaceAgentsEnabled: true, WorkspaceAgentsFileName: "AGENTS.md"}
	if got := load(enabled); got != "project rules" {
		t.Fatalf("prompt = %q", got)
	}
	disabled := enabled
	disabled.WorkspaceAgentsEnabled = false
	chatType := enabled
	chatType.WorkspaceRoot = ""
	otherMode := enabled
	otherMode.Mode = "KBASE"
	missing := enabled
	missing.WorkspaceAgentsFileName = "MISSING.md"
	for name, policy := range map[string]WorkspacePromptPolicy{"disabled": disabled, "chat type": chatType, "other mode": otherMode, "missing file": missing} {
		if got := load(policy); got != "" {
			t.Fatalf("%s: prompt = %q, want empty", name, got)
		}
	}
	escape := enabled
	escape.WorkspaceAgentsFileName = "../AGENTS.md"
	if _, err := LoadWorkspacePrompt(escape); err == nil {
		t.Fatalf("expected path escape rejection")
	}
}
