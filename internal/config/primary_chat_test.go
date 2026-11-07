package config

import "testing"

func TestBotConfigurationOwnership(t *testing.T) {
	cfg := defaultConfig(LoadOptions{})
	if cfg.Bot.SystemPrompt != "" {
		t.Fatal("deployment prompt must only live in example YAML")
	}
	for _, body := range []string{"primary-chat: {}\n", "primary-chat:\n  enabled: false\n"} {
		if err := cfg.applyRuntimeFile(configFixture(t, "runtime.yml", body)); err == nil {
			t.Fatal("retired primary-chat accepted")
		}
	}
	if err := cfg.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "bot:\n  dispatch:\n    modelKey: scheduler\n    reasoningEffort: LOW\n  preset-tools: [bash]\n")); err != nil {
		t.Fatal(err)
	}
	if err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "bot:\n  system-prompt: Schedule work\n")); err != nil {
		t.Fatal(err)
	}
	if cfg.Bot.ModelKey != "scheduler" || cfg.Bot.SystemPrompt != "Schedule work" || len(cfg.ModePresets["bot"].Tools) != 1 {
		t.Fatal(cfg.Bot)
	}
	if err := cfg.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "primary: {}\n")); err == nil {
		t.Fatal("retired primary accepted")
	}
	if err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "primary: {}\n")); err == nil {
		t.Fatal("retired primary accepted")
	}
}
