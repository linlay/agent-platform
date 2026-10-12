package config

import (
	"strings"
	"testing"
)

func TestRejectEditingPromptAndVariable(t *testing.T) {
	for _, body := range []string{
		"kbase:\n  editing-prompt: old\n",
		"kbase:\n  workspace-prompt: '{{editing_mode}}'\n",
		"coder:\n  system-prompt: '{{ editing_mode }}'\n",
		"shared:\n  planning-mode:\n    planning-prompt: '{{editing_mode}}'\n",
	} {
		cfg := Config{KBasePrompts: KBasePromptsConfig{WorkspacePrompt: "current"}}
		err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", body))
		if err == nil || !(strings.Contains(err.Error(), "editing-prompt") || strings.Contains(err.Error(), "editing_mode")) {
			t.Fatalf("retired prompt configuration accepted: %v", err)
		}
		if cfg.KBasePrompts.WorkspacePrompt != "current" {
			t.Fatal("rejected configuration modified the active prompt")
		}
	}
}
