package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusIndexesAndRuntimeStateAreOptional(t *testing.T) {
	minimal, err := json.Marshal(Status{AgentKey: "docs", Mode: Mode})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"engine", "indexes", "sidecar"} {
		if strings.Contains(string(minimal), `"`+field+`"`) {
			t.Fatalf("optional field %q unexpectedly present in %s", field, minimal)
		}
	}
	pending := 2
	status := Status{
		AgentKey: "docs", Mode: Mode, Engine: "kbx",
		Indexes: &IndexesStatus{
			FTS:    IndexStatus{Type: "fts", Ready: true},
			Vector: VectorIndexStatus{Type: "vector", PendingContentUnits: &pending},
		},
		Sidecar: &RuntimeState{Engine: "kbx", Available: true},
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"engine":"kbx"`,
		`"indexes":{"fts":{"type":"fts","ready":true},"vector":{"type":"vector","ready":false,"pendingContentUnits":2}}`,
		`"sidecar":{"engine":"kbx","available":true}`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("status JSON %s does not contain %s", encoded, want)
		}
	}
}
