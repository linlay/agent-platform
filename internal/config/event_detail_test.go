package config

import "testing"

func TestModeDefaultDetailSettings(t *testing.T) {
	c := Config{}
	if err := c.applyAgentSettingsFile(configFixture(t, "agent-settings.yml", "coder:\n  interaction-config:\n    default-detail: full\ngeneral:\n  interaction-config:\n    default-detail: summary\nplan-execute:\n  interaction-config:\n    default-detail: full\n")); err != nil {
		t.Fatal(err)
	}
	if c.ModeDefaultDetails["coder"] != "full" || c.ModeDefaultDetails["general"] != "summary" || c.ModeDefaultDetails["plan-execute"] != "full" {
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
