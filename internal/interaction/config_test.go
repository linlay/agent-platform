package interaction

import "testing"

func TestModeDefaultsAndOverrides(t *testing.T) {
	for _, mode := range []string{"REACT", "CODER", "KBASE"} {
		t.Run(mode, func(t *testing.T) {
			c, err := Parse(mode, nil)
			if err != nil {
				t.Fatal(err)
			}
			kbase := mode == "KBASE"
			if c.Model == kbase || c.AccessLevel == kbase || c.Connectors == kbase || !c.MustUseSkills || !c.Attachment.LocalFiles || c.Attachment.ChatRecords != (mode == "REACT") {
				t.Fatalf("defaults: %+v", c)
			}
			overridden, err := Parse(mode, map[string]any{"model": false, "mustUseSkills": false, "attachment": map[string]any{"localFiles": false}})
			if err != nil || overridden.Model || overridden.MustUseSkills || overridden.Attachment.LocalFiles || overridden.AccessLevel != c.AccessLevel {
				t.Fatalf("override: %+v %v", overridden, err)
			}
		})
	}
	c, err := Parse("KBASE", map[string]any{"model": true, "accessLevel": true, "connectors": true, "attachment": map[string]any{"chatRecords": true}})
	if err != nil || !c.Model || !c.AccessLevel || !c.Connectors || !c.Attachment.ChatRecords {
		t.Fatalf("enable KBASE: %+v %v", c, err)
	}
}
func TestRejectMalformedInteractionConfig(t *testing.T) {
	for _, raw := range []any{true, map[string]any{"model": "false"}, map[string]any{"models": true}, map[string]any{"attachment": true}, map[string]any{"attachment": map[string]any{"driveFiles": true}}, map[string]any{"attachment": map[string]any{"localFiles": nil}}} {
		if _, err := Parse("REACT", raw); err == nil {
			t.Fatalf("accepted malformed config %#v", raw)
		}
	}
}

func TestMustUseKbasesDefaultsAndBooleanOverride(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM", "PROXY", "CHANNEL"} {
		c, err := Parse(mode, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := mode == "GENERAL" || mode == "CODER"
		if c.MustUseKbases != want {
			t.Fatalf("%s default: %+v", mode, c)
		}
		c, err = Parse(mode, map[string]any{"mustUseKbases": true})
		if err != nil || !c.MustUseKbases {
			t.Fatalf("override %s: %+v %v", mode, c, err)
		}
	}
	if _, err := Parse("GENERAL", map[string]any{"mustUseKbases": "true"}); err == nil {
		t.Fatal("nonboolean switch accepted")
	}
}

func TestEntryVisibilityDefaultsAndOverrides(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		c := Defaults(mode)
		if !c.ShowPlanningMode || c.ShowEditingMode != (mode == "KBASE") || c.ShowTerminal != (mode == "CODER") {
			t.Fatalf("%s entry defaults = %+v", mode, c)
		}
	}
	if c := DefaultsForEngine("KBASE", true); !c.ShowPlanningMode || c.ShowEditingMode {
		t.Fatalf("ACP entry defaults = %+v", c)
	}
	if c := Defaults("PROXY"); c.ShowPlanningMode || c.ShowEditingMode || c.ShowTerminal {
		t.Fatalf("PROXY must not offer entries by default: %+v", c)
	}
	// An explicit value wins over the mode's default in both directions.
	c, err := Parse("KBASE", map[string]any{"showEditingMode": false, "showTerminal": true})
	if err != nil || c.ShowEditingMode || !c.ShowTerminal || !c.ShowPlanningMode {
		t.Fatalf("explicit override = %+v err=%v", c, err)
	}
	// The entries are display-only: they never reject a request.
	if err := c.Validate(false, "default", false); err != nil {
		t.Fatalf("entry visibility must not gate requests: %v", err)
	}
}
