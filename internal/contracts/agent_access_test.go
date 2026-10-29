package contracts

import "testing"

func TestAgentAccessConfigValidationAndResolution(t *testing.T) {
	for _, raw := range []any{nil, "full_access", []any{}, map[string]any{"default": nil}, map[string]any{"default": ""}, map[string]any{"default": "FULL_ACCESS"}, map[string]any{"defaultLevel": "default"}, map[string]any{"cap": "default"}} {
		if _, err := ParseAgentAccessConfig(map[string]any{"accessConfig": raw}); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
	for _, configured := range []string{"", AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		definition := map[string]any{}
		if configured != "" {
			definition["accessConfig"] = map[string]any{"default": configured}
		}
		c, err := ParseAgentAccessConfig(definition)
		if err != nil {
			t.Fatal(err)
		}
		for _, requested := range []string{"", AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
			level, source, err := ResolveInitialAccessLevel(requested, c)
			want, wantSource := requested, "explicit"
			if requested == "" {
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
