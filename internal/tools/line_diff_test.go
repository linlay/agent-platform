package tools

import "testing"

func TestComputeLineDiffStatsTreatsMissingFinalNewlineAsSameLine(t *testing.T) {
	stats := computeLineDiffStats("alpha", "alpha\n")
	if stats.AddedLines != 0 || stats.DeletedLines != 0 || stats.EditedLines != 0 {
		t.Fatalf("expected final newline-only change to have zero line stats, got %#v", stats)
	}
}

func TestComputeLineDiffStatsCountsSeparateHunks(t *testing.T) {
	stats := computeLineDiffStats("one\nsame\none\n", "two\nsame\ntwo\n")
	if stats.AddedLines != 2 || stats.DeletedLines != 2 || stats.EditedLines != 2 {
		t.Fatalf("expected two edited hunks, got %#v", stats)
	}
}
