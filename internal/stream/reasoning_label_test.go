package stream

import "testing"

func TestReasoningLabelForIDIsDeterministic(t *testing.T) {
	first := ReasoningLabelForID("run_1_r_1")
	second := ReasoningLabelForID("run_1_r_1")
	if first == "" {
		t.Fatal("expected reasoning label to be non-empty")
	}
	if first != second {
		t.Fatalf("expected deterministic reasoning label, got %q and %q", first, second)
	}
}

func TestReasoningLabelForIDFallsBackForEmptyID(t *testing.T) {
	if got := ReasoningLabelForID(""); got != "正在思考" {
		t.Fatalf("expected first reasoning label for empty id fallback, got %q", got)
	}
}
