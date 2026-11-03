package terminal

import (
	"strings"
	"testing"
)

func TestTerminalNeverReceivesIdentity(t *testing.T) {
	t.Setenv("AP_ID_TOKEN", "inherited-token")
	for _, managed := range []bool{false, true} {
		env := processEnvironment(startPTYRequest{Managed: managed, Env: []string{"ap_id_token=override", "AP_ID_TOKEN=duplicate", "AP_CHAT_DIR=chat", "KEEP=public"}})
		for _, entry := range env {
			key, _, _ := strings.Cut(entry, "=")
			if strings.EqualFold(key, "AP_ID_TOKEN") || strings.EqualFold(key, "AP_CHAT_DIR") {
				t.Fatal("terminal received chat identity")
			}
		}
	}
}

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
