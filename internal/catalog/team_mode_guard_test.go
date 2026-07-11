package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrdinaryAgentCannotDeclareInternalTeamMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yml")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		"key: fake-team",
		"mode: TEAM",
		"modelConfig:",
		"  modelKey: mock-model",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := parseAgentFileRaw(path)
	if err == nil || !strings.Contains(err.Error(), "mode TEAM is internal") {
		t.Fatalf("expected internal TEAM mode rejection, got %v", err)
	}
}
