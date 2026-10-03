package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPinProvenanceIsAssignedOnReadOnly(t *testing.T) {
	s := Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors-center")}
	digest := strings.Repeat("a", 64)
	mount := AgentRuntime{AgentKey: "demo", ID: "example", Digest: digest, Dir: filepath.Join(s.SharedRoot(), "example", digest), FromRunPin: true}
	if err := s.PinRun("run", []AgentRuntime{mount}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.RunPinPath("run"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "FromRunPin") {
		t.Fatal("runtime provenance must not be persisted")
	}
	pins, err := s.PinnedRuntimes()
	if err != nil || len(pins) != 1 || pins[0] != mount {
		t.Fatalf("pin provenance missing: %#v %v", pins, err)
	}
	var forged AgentRuntime
	if err := json.Unmarshal([]byte(`{"AgentKey":"demo","FromRunPin":true}`), &forged); err != nil {
		t.Fatal(err)
	}
	if forged.FromRunPin {
		t.Fatal("JSON must not set runtime provenance")
	}
}
