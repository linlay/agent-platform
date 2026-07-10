package terminal

import "testing"

func TestMergeEnvironmentUsesOverridesWithoutDuplicates(t *testing.T) {
	got := mergeEnvironment(
		[]string{"PATH=/system/bin", "XDG_CONFIG_HOME=/system/config", "KEEP=value"},
		[]string{"XDG_CONFIG_HOME=/agent/config", "TERM=xterm-256color"},
	)
	if len(got) != 4 {
		t.Fatalf("merged env length = %d, want 4: %#v", len(got), got)
	}
	if got[1] != "XDG_CONFIG_HOME=/agent/config" {
		t.Fatalf("XDG_CONFIG_HOME was not replaced: %#v", got)
	}
	if got[3] != "TERM=xterm-256color" {
		t.Fatalf("TERM was not appended: %#v", got)
	}
}
