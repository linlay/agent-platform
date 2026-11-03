package config

import "testing"

func TestModeDefaultDetailSettings(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "coder:\n  interaction-config:\n    default-detail: detailed\ngeneral:\n  interaction-config:\n    default-detail: simple\nproxy:\n  interaction-config:\n    default-detail: detailed\n")); err != nil {
		t.Fatal(err)
	}
	if c.ModeDefaultDetails["coder"] != "detailed" || c.ModeDefaultDetails["general"] != "simple" || c.ModeDefaultDetails["proxy"] != "detailed" {
		t.Fatal(c.ModeDefaultDetails)
	}
	for _, bad := range []string{"null", "true", "verbose", "none", "summary", "full", "''", "[]"} {
		if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "coder:\n  interaction-config:\n    default-detail: "+bad+"\n")); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := c.applyAgentSettingsFile(ProjectFile("configs/agent-settings.example.yml")); err != nil {
		t.Fatal(err)
	}
	if c.ModeDefaultDetails["coder"] != "detailed" || c.ModeDefaultDetails["team"] != "detailed" {
		t.Fatal(c.ModeDefaultDetails)
	}
}

func TestRemovedAnnounceHoldSettingIsRejected(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "announce-hold-seconds: 60\n")); err == nil {
		t.Fatal("removed announce-hold-seconds was accepted")
	}
}

func TestRetiredPlanExecuteSectionsAreIgnored(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "plan-execute:\n  interaction-config:\n    default-detail: detailed\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ModeDefaultDetails["plan-execute"]; ok {
		t.Fatal(c.ModeDefaultDetails)
	}
	if err := c.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "shared:\n  plan-execute:\n    summary-system-prompt: old\n")); err != nil {
		t.Fatal(err)
	}
}
