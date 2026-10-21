package contracts

import (
	"reflect"
	"testing"
)

func TestCountTextLines(t *testing.T) {
	for content, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2, "\n": 1, "\n\n": 2} {
		if got := CountTextLines(content); got != want {
			t.Fatalf("CountTextLines(%q)=%d want %d", content, got, want)
		}
	}
}

func TestReadFileSnapshotCoverage(t *testing.T) {
	snap := ReadFileSnapshot{TotalLines: 10, Ranges: []LineRange{{Start: 4, End: 6}, {Start: 1, End: 2}, {Start: 7, End: 8}}}
	if !snap.Covers(4, 8) || snap.Covers(2, 4) || snap.Complete() {
		t.Fatalf("unexpected coverage for %#v", snap)
	}
	if got, want := snap.Uncovered(1, 10), []LineRange{{Start: 3, End: 3}, {Start: 9, End: 10}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Uncovered=%#v want %#v", got, want)
	}
	if !(ReadFileSnapshot{}).Complete() {
		t.Fatal("empty file version must count as complete")
	}
}

func TestMergeReadFileSnapshotAccumulatesSameVersion(t *testing.T) {
	first := ReadFileSnapshot{SHA256: "a", TotalLines: 9, Ranges: []LineRange{{Start: 1, End: 3}}, ReadAtUnixMs: 1}
	second := ReadFileSnapshot{SHA256: "a", TotalLines: 9, Ranges: []LineRange{{Start: 4, End: 9}}, ReadAtUnixMs: 2, LastRead: ReadWindow{Offset: 4}}
	merged := MergeReadFileSnapshot(first, second)
	if !merged.Complete() || merged.ReadAtUnixMs != 2 || merged.LastRead.Offset != 4 {
		t.Fatalf("unexpected merge %#v", merged)
	}
	// Concurrent completion order must not lose pages or pick the older read.
	if reversed := MergeReadFileSnapshot(second, first); !reversed.Complete() || reversed.ReadAtUnixMs != 2 {
		t.Fatalf("unexpected reversed merge %#v", reversed)
	}
	changed := ReadFileSnapshot{SHA256: "b", TotalLines: 9, Ranges: []LineRange{{Start: 1, End: 1}}, ReadAtUnixMs: 3}
	if got := MergeReadFileSnapshot(merged, changed); got.SHA256 != "b" || got.Complete() {
		t.Fatalf("new version must replace old ranges, got %#v", got)
	}
	if got := MergeReadFileSnapshot(changed, merged); got.SHA256 != "b" {
		t.Fatalf("later observation must win, got %#v", got)
	}
	merged.Ranges[0].End = 1
	if second.Ranges[0].Start != 4 || first.Ranges[0].End != 3 {
		t.Fatal("merge aliased input ranges")
	}
}

func TestApplyLineEditsShiftsCoverage(t *testing.T) {
	ranges := []LineRange{{Start: 1, End: 5}, {Start: 10, End: 12}}
	// Lines 2..3 become four lines, then line 11 is deleted.
	got := ApplyLineEdits(ranges, []LineEdit{{Start: 2, End: 3, Delta: 2}, {Start: 11, End: 11, Delta: -1}})
	if want := []LineRange{{Start: 1, End: 7}, {Start: 12, End: 13}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyLineEdits=%#v want %#v", got, want)
	}
	// Collapsing the last covered line keeps the replacement line covered.
	if got := ApplyLineEdits([]LineRange{{Start: 2, End: 2}}, []LineEdit{{Start: 2, End: 2, Delta: -1}}); !reflect.DeepEqual(got, []LineRange{{Start: 2, End: 2}}) {
		t.Fatalf("unexpected collapse result %#v", got)
	}
}
