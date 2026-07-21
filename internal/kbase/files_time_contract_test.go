package kbase

import (
	"testing"

	"agent-platform/internal/timecontract"
)

func TestFilesResultOmitsInternalAbsentFileTimes(t *testing.T) {
	options := FilesOptions{Mode: "files", Status: "all", Pattern: "**", HeadLimit: 0}
	result, err := filesResultFromRecords(emptyFilesResult(options), options, []fileRecord{{
		ID:        "file_recovery",
		Path:      "recovery.md",
		Status:    "deleted",
		MTimeMS:   -1, // pending recovery sentinel, never a public instant
		IndexedAt: 0,  // not yet indexed
	}})
	if err != nil {
		t.Fatalf("map files: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].MTimeMS != nil || result.Results[0].IndexedAt != nil {
		t.Fatalf("expected absent internal times to be omitted, got %#v", result.Results)
	}
}

func TestFilesResultRejectsHistoricSeconds(t *testing.T) {
	options := FilesOptions{Mode: "files", Status: "all", Pattern: "**", HeadLimit: 0}
	_, err := filesResultFromRecords(emptyFilesResult(options), options, []fileRecord{{
		ID:      "file_seconds",
		Path:    "old.md",
		Status:  "active",
		MTimeMS: 1_700_000_000,
	}})
	if !timecontract.IsViolation(err) {
		t.Fatalf("expected epoch-milliseconds violation, got %v", err)
	}
}
