package config

import "testing"

func TestPrimaryChatConfigurationOwnership(t *testing.T) {
	cfg := defaultConfig(LoadOptions{})
	if cfg.PrimaryChat.Enabled {
		t.Fatal("must default off")
	}
	if err := cfg.applyRuntimeFile(configFixture(t, "runtime.yml", "primary-chat:\n  enabled: true\n")); err != nil {
		t.Fatal(err)
	}
	if err := cfg.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "primary:\n  modelKey: scheduler\n  reasoningEffort: LOW\n")); err != nil {
		t.Fatal(err)
	}
	if err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "primary:\n  system-prompt: Schedule work\n")); err != nil {
		t.Fatal(err)
	}
	if !cfg.PrimaryChat.Enabled || cfg.PrimaryChat.ModelKey != "scheduler" || cfg.PrimaryChat.SystemPrompt != "Schedule work" {
		t.Fatal(cfg.PrimaryChat)
	}
	for _, body := range []string{"primary-chat:\n  enabled: \"true\"\n", "primary-chat:\n  modelKey: x\n"} {
		if err := cfg.applyRuntimeFile(configFixture(t, "runtime.yml", body)); err == nil {
			t.Fatal("invalid runtime policy accepted", body)
		}
	}
	if err := cfg.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "primary:\n  system-prompt: x\n")); err == nil {
		t.Fatal("prompt accepted in settings")
	}
}
