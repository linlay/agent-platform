package interaction

import "testing"

func TestAgentDefaultDetailOverride(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM", "PROXY"} {
		c, err := ParseForEngine(mode, false, nil)
		if err != nil || c.DefaultDetail != "summary" {
			t.Fatalf("%s: %+v %v", mode, c, err)
		}
		c, err = ParseForEngine(mode, false, map[string]any{"defaultDetail": "full"})
		if err != nil || c.DefaultDetail != "full" {
			t.Fatalf("%s: %+v %v", mode, c, err)
		}
		c, err = ParseForEngine(mode, false, map[string]any{"defaultDetail": "summary"})
		if err != nil || c.DefaultDetail != "summary" {
			t.Fatalf("%s: %+v %v", mode, c, err)
		}
	}
	for _, raw := range []any{nil, true, "", "verbose", "none"} {
		if _, err := ParseForEngine("CODER", false, map[string]any{"defaultDetail": raw}); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
}
