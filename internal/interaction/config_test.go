package interaction

import "testing"

func TestModeDefaultsAndOverrides(t *testing.T) {
	for _, mode := range []string{"REACT", "CODER", "KBASE"} {
		t.Run(mode, func(t *testing.T) {
			c, err := ParseForEngine(mode, "native", nil)
			if err != nil {
				t.Fatal(err)
			}
			kbase := mode == "KBASE"
			if c.Model == kbase || c.AccessLevel == kbase || c.Connectors == kbase || !c.Attachment.Skills || !c.Attachment.LocalFiles || c.Attachment.ChatRecords != (mode == "REACT") {
				t.Fatalf("defaults: %+v", c)
			}
			overridden, err := ParseForEngine(mode, "native", map[string]any{"model": false, "attachment": map[string]any{"localFiles": false, "skills": false}})
			if err != nil || overridden.Model || overridden.Attachment.Skills || overridden.Attachment.LocalFiles || overridden.AccessLevel != c.AccessLevel {
				t.Fatalf("override: %+v %v", overridden, err)
			}
		})
	}
	c, err := ParseForEngine("KBASE", "native", map[string]any{"model": true, "accessLevel": true, "connectors": true, "attachment": map[string]any{"chatRecords": true}})
	if err != nil || !c.Model || !c.AccessLevel || !c.Connectors || !c.Attachment.ChatRecords {
		t.Fatalf("enable KBASE: %+v %v", c, err)
	}
}
func TestRejectMalformedInteractionConfig(t *testing.T) {
	for _, raw := range []any{true, map[string]any{"model": "false"}, map[string]any{"models": true}, map[string]any{"attachment": true}, map[string]any{"attachment": map[string]any{"driveFiles": true}}, map[string]any{"attachment": map[string]any{"localFiles": nil}}} {
		if _, err := ParseForEngine("REACT", "native", raw); err == nil {
			t.Fatalf("accepted malformed config %#v", raw)
		}
	}
}

func TestReferenceKbasesDefaultsAndBooleanOverride(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM", "PROXY", "CHANNEL"} {
		c, err := ParseForEngine(mode, "native", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := mode == "GENERAL" || mode == "CODER" || mode == "TEAM"
		if c.Attachment.Kbases != want {
			t.Fatalf("%s default: %+v", mode, c)
		}
		c, err = ParseForEngine(mode, "native", map[string]any{"attachment": map[string]any{"kbases": true}})
		if err != nil || c.Attachment.Kbases != (mode != "PROXY" && mode != "CHANNEL") {
			t.Fatalf("override %s: %+v %v", mode, c, err)
		}
	}
	if _, err := ParseForEngine("GENERAL", "native", map[string]any{"unexpectedOption": "true"}); err == nil {
		t.Fatal("unknown switch accepted")
	}
}

func TestRemoteBackendsDoNotExposeLocalResourceReferences(t *testing.T) {
	for _, backend := range []struct {
		mode   string
		engine string
	}{{"PROXY", "native"}, {"CHANNEL", "native"}} {
		for _, raw := range []any{nil, map[string]any{"attachment": map[string]any{"skills": true, "kbases": true, "chatRecords": true}}} {
			c, err := ParseForEngine(backend.mode, backend.engine, raw)
			if err != nil || c.Attachment.Skills || c.Attachment.Kbases || c.Attachment.ChatRecords || c.Attachment.LocalFiles == (backend.engine == "acp") {
				t.Fatalf("backend=%+v config=%+v err=%v", backend, c, err)
			}
		}
	}
}

func TestEntryVisibilityDefaultsAndOverrides(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		c := DefaultsForEngine(mode, "native")
		if !c.ShowPlanningMode || c.ShowEditingMode != (mode == "KBASE") || c.ShowTerminal != (mode == "CODER") {
			t.Fatalf("%s entry defaults = %+v", mode, c)
		}
	}
	if c := DefaultsForEngine("", "acp"); c.ShowPlanningMode || c.ShowEditingMode {
		t.Fatalf("ACP entry defaults = %+v", c)
	}
	if c := DefaultsForEngine("PROXY", "native"); c.ShowPlanningMode || c.ShowEditingMode || c.ShowTerminal {
		t.Fatalf("PROXY must not offer entries by default: %+v", c)
	}
	// An explicit value wins over the mode's default in both directions.
	c, err := ParseForEngine("KBASE", "native", map[string]any{"showEditingMode": false, "showTerminal": true})
	if err != nil || c.ShowEditingMode || !c.ShowTerminal || !c.ShowPlanningMode {
		t.Fatalf("explicit override = %+v err=%v", c, err)
	}
	// The entries are display-only: they never reject a request.
	if err := c.Validate(false, false); err != nil {
		t.Fatalf("entry visibility must not gate requests: %v", err)
	}
}

func TestACPContextInputsReusePlatformWhileNativeControlsStayClosed(t *testing.T) {
	c, err := ParseForEngine("", "acp", map[string]any{"model": true, "accessLevel": true, "connectors": true, "showPlanningMode": true, "showEditingMode": true, "showTerminal": true, "attachment": map[string]any{"localFiles": true}})
	if err != nil || c.DefaultDetail != "simple" || c.Model || c.AccessLevel || !c.Connectors || c.ShowPlanningMode || c.ShowEditingMode || c.ShowTerminal || !c.Attachment.LocalFiles || !c.Attachment.Skills || !c.Attachment.Kbases || c.Attachment.ChatRecords {
		t.Fatalf("unconfirmed capabilities: %+v %v", c, err)
	}
}

func TestACPExplicitContextSwitchesRemainDisabled(t *testing.T) {
	c, err := ParseForEngine("", "acp", map[string]any{"connectors": false, "attachment": map[string]any{"localFiles": false, "skills": false, "kbases": false}})
	if err != nil || c.Connectors || c.Attachment.LocalFiles || c.Attachment.Skills || c.Attachment.Kbases {
		t.Fatalf("override ignored: %+v %v", c, err)
	}
}
