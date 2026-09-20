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
