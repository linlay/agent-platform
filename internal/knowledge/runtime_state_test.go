package knowledge

import (
	"encoding/json"
	"testing"
)

func TestRuntimeStatePreservesSidecarJSONContract(t *testing.T) {
	state := RuntimeState{Engine: "kbx", Available: true, ProtocolVersion: 2, EngineVersion: "fixture", LanceDBVersion: "legacy", LastError: "diagnostic"}
	b, err := json.Marshal(struct {
		Sidecar RuntimeState `json:"sidecar"`
	}{state})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sidecar":{"engine":"kbx","available":true,"protocolVersion":2,"engineVersion":"fixture","lancedbVersion":"legacy","lastError":"diagnostic"}}`
	if string(b) != want {
		t.Fatalf("sidecar JSON changed: %s", b)
	}
	b, err = json.Marshal(RuntimeState{Engine: "kbx", Available: true})
	if err != nil || string(b) != `{"engine":"kbx","available":true}` {
		t.Fatalf("empty compatibility fields must remain omitted: %s, %v", b, err)
	}
}

func TestKBXStatusDoesNotInventChunkOrVectorRowCounts(t *testing.T) {
	pending := 3
	b, err := json.Marshal(Status{Engine: "kbx", Indexes: &IndexesStatus{Vector: VectorIndexStatus{Type: "vector", PendingContentUnits: &pending}}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["chunks"]; exists || string(fields["chunksKnown"]) != "false" {
		t.Fatalf("unknown chunk count changed: %s", b)
	}
	var indexes struct{ Vector map[string]json.RawMessage }
	if err := json.Unmarshal(fields["indexes"], &indexes); err != nil {
		t.Fatal(err)
	}
	if _, exists := indexes.Vector["unindexedRows"]; exists || string(indexes.Vector["pendingContentUnits"]) != "3" {
		t.Fatalf("content units must not become row counts: %s", b)
	}
}
