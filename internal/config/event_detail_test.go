package config

import "testing"

func TestModeDefaultDetailSettings(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "coder:\n  interaction-config:\n    default-detail: full\ngeneral:\n  interaction-config:\n    default-detail: summary\nproxy:\n  interaction-config:\n    default-detail: full\n")); err != nil {
		t.Fatal(err)
	}
	if c.ModeDefaultDetails["coder"] != "full" || c.ModeDefaultDetails["general"] != "summary" || c.ModeDefaultDetails["proxy"] != "full" {
		t.Fatal(c.ModeDefaultDetails)
	}
	for _, bad := range []string{"null", "true", "verbose", "''", "[]"} {
		if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "coder:\n  interaction-config:\n    default-detail: "+bad+"\n")); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := c.applyAgentSettingsFile(ProjectFile("configs/agent-settings.example.yml")); err != nil {
		t.Fatal(err)
	}
	if c.ModeDefaultDetails["coder"] != "full" || c.ModeDefaultDetails["team"] != "full" {
		t.Fatal(c.ModeDefaultDetails)
	}
}

func TestModeDefaultDetailAcceptsNone(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "general:\n  interaction-config:\n    default-detail: none\n")); err != nil {
		t.Fatal(err)
	}
	if c.ModeDefaultDetails["general"] != "none" {
		t.Fatal(c.ModeDefaultDetails)
	}
}

func TestAnnounceHoldSeconds(t *testing.T) {
	c := Config{AnnounceHoldSeconds: DefaultAnnounceHoldSeconds}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "general: {}\n")); err != nil {
		t.Fatal(err)
	}
	if c.AnnounceHold().Seconds() != DefaultAnnounceHoldSeconds {
		t.Fatalf("omitted key changed the hold: %v", c.AnnounceHold())
	}
	for raw, want := range map[string]int{"0": 0, "90": 90} {
		if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "announce-hold-seconds: "+raw+"\n")); err != nil {
			t.Fatal(err)
		}
		if c.AnnounceHoldSeconds != want {
			t.Fatalf("announce-hold-seconds %s = %d", raw, c.AnnounceHoldSeconds)
		}
	}
	for _, bad := range []string{"-1", "1.5", "'60'", "null", "[]"} {
		if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "announce-hold-seconds: "+bad+"\n")); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestRetiredPlanExecuteSectionsAreIgnored(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "plan-execute:\n  interaction-config:\n    default-detail: full\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ModeDefaultDetails["plan-execute"]; ok {
		t.Fatal(c.ModeDefaultDetails)
	}
	if err := c.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "shared:\n  plan-execute:\n    summary-system-prompt: old\n")); err != nil {
		t.Fatal(err)
	}
}
