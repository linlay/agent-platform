package kbase

import (
	"encoding/json"
	"strings"
	"testing"
)

func int64Pointer(value int64) *int64 {
	return &value
}

func TestStatusLanceFieldsAreOptionalAndStable(t *testing.T) {
	minimal, err := json.Marshal(Status{AgentKey: "docs", Mode: Mode})
	if err != nil {
		t.Fatalf("marshal minimal status: %v", err)
	}
	for _, field := range []string{"engine", "schemaVersion", "generation", "migration", "indexes", "legacyAvailable"} {
		if strings.Contains(string(minimal), `"`+field+`"`) {
			t.Fatalf("optional field %q unexpectedly present in %s", field, minimal)
		}
	}

	status := Status{
		AgentKey:      "docs",
		Mode:          Mode,
		Engine:        "lancedb",
		SchemaVersion: "3",
		Generation: &GenerationStatus{
			ID:           "kbg_1",
			State:        "active",
			TableVersion: 12,
			CreatedAt:    1_700_000_000_000,
			ActivatedAt:  1_700_000_001_000,
		},
		Migration: &MigrationStatus{
			State:          "idle",
			Progress:       1,
			ImportedFiles:  3,
			TotalFiles:     3,
			ImportedChunks: 9,
			TotalChunks:    9,
		},
		Indexes: &IndexesStatus{
			FTS:             IndexStatus{Type: "FTS/ICU", Ready: true},
			Vector:          VectorIndexStatus{Type: "flat", Ready: true, UnindexedRows: 0},
			LastOptimizedAt: int64Pointer(1_700_000_002_000),
		},
		LegacyAvailable: true,
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal Lance status: %v", err)
	}
	for _, want := range []string{
		`"engine":"lancedb"`,
		`"schemaVersion":"3"`,
		`"generation":{"id":"kbg_1","state":"active","tableVersion":12,"createdAt":1700000000000,"activatedAt":1700000001000}`,
		`"migration":{"state":"idle","progress":1,"importedFiles":3,"totalFiles":3,"importedChunks":9,"totalChunks":9}`,
		`"indexes":{"fts":{"type":"FTS/ICU","ready":true},"vector":{"type":"flat","ready":true,"unindexedRows":0},"lastOptimizedAt":1700000002000}`,
		`"legacyAvailable":true`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("status JSON %s does not contain %s", encoded, want)
		}
	}
}

func TestKBasePublicMetadataTimeIsStrictAndOptional(t *testing.T) {
	value, err := parseOptionalPublicEpochMillis("", "lastIndexedAt", "test")
	if err != nil || value != nil {
		t.Fatalf("expected missing metadata to be omitted, got %#v %v", value, err)
	}
	if _, err := parseOptionalPublicEpochMillis("1700000000", "lastIndexedAt", "test"); err == nil {
		t.Fatal("expected seconds metadata to be rejected")
	}
	value, err = parseOptionalPublicEpochMillis("1700000000000", "lastIndexedAt", "test")
	if err != nil || value == nil || *value != 1_700_000_000_000 {
		t.Fatalf("expected valid epoch-ms metadata, got %#v %v", value, err)
	}
}
