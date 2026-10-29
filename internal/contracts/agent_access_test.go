package contracts

import "testing"

func TestAgentAccessConfigValidationAndResolution(t *testing.T) {
	for _, raw := range []any{nil, "full_access", []any{}, map[string]any{"default": nil}, map[string]any{"default": ""}, map[string]any{"default": "FULL_ACCESS"}, map[string]any{"defaultLevel": "default"}, map[string]any{"cap": "default"}} {
		if _, err := ParseAgentAccessConfig(map[string]any{"accessConfig": raw}); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
	for _, configured := range []string{"", AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		definition := map[string]any{}
		if configured != "" {
			definition["accessConfig"] = map[string]any{"default": configured}
		}
		c, err := ParseAgentAccessConfig(definition)
		if err != nil {
			t.Fatal(err)
		}
		for _, requested := range []string{"", AccessLevelDefault, AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
			level, source, err := ResolveInitialAccessLevel(requested, c)
			want, wantSource := requested, "explicit"
			if IsDefaultAccessLevelRequest(requested) {
				want, wantSource = c.EffectiveDefault(), "agent_default"
				if configured == "" {
					wantSource = "platform_default"
				}
			}
			if err != nil || level != want || source != wantSource {
				t.Fatalf("config=%q request=%q: %s %s %v", configured, requested, level, source, err)
			}
		}
	}
	if err := ValidateExplicitRunAccess("full_access", "default"); err == nil {
		t.Fatal("explicit escalation allowed")
	}
	if err := ValidateExplicitRunAccess("", "default"); err != nil {
		t.Fatal("omitted target default was capped", err)
	}
}

func TestDefaultSelectorNeverBecomesAnEffectivePermission(t *testing.T) {
	if _, ok := NormalizeAccessLevel(AccessLevelDefault); ok {
		t.Fatal("request selector accepted as a concrete execution level")
	}
	for _, requested := range []string{"", "default", " DEFAULT "} {
		level, source, err := ResolveInitialAccessLevel(requested, AgentAccessConfig{Default: AccessLevelFullAccess})
		if err != nil || level != AccessLevelFullAccess || source != "agent_default" {
			t.Fatalf("selector %q: %s %s %v", requested, level, source, err)
		}
		level, source, err = ResolveInitialAccessLevel(requested, AgentAccessConfig{})
		if err != nil || level != AccessLevelAskForApproval || source != "platform_default" {
			t.Fatalf("unconfigured selector %q: %s %s %v", requested, level, source, err)
		}
		if err := ValidateExplicitRunAccess(requested, AccessLevelAskForApproval); err != nil {
			t.Fatal("default selector was treated as an explicit escalation", err)
		}
	}
	c, err := ParseAgentAccessConfig(map[string]any{"accessConfig": map[string]any{"default": "default"}})
	if err != nil || c.Default != AccessLevelAskForApproval {
		t.Fatal("legacy Agent config changed permission", c, err)
	}
}
