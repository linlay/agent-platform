package interaction

import "testing"

func TestAgentDefaultDetailOverride(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM", "PROXY"} {
		c, err := ParseForEngine(mode, "native", nil)
		if err != nil || c.DefaultDetail != "simple" {
			t.Fatalf("%s: %+v %v", mode, c, err)
		}
		c, err = ParseForEngine(mode, "native", map[string]any{"defaultDetail": "detailed"})
		if err != nil || c.DefaultDetail != "detailed" {
			t.Fatalf("%s: %+v %v", mode, c, err)
		}
		c, err = ParseForEngine(mode, "native", map[string]any{"defaultDetail": "simple"})
		if err != nil || c.DefaultDetail != "simple" {
			t.Fatalf("%s: %+v %v", mode, c, err)
		}
	}
	for _, raw := range []any{nil, true, "", "verbose", "none", "summary", "full"} {
		if _, err := ParseForEngine("CODER", "native", map[string]any{"defaultDetail": raw}); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
}
