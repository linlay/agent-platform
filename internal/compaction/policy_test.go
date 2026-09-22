package compaction

import "testing"

func TestThresholdBoundaries(t *testing.T) {
	for _, tc := range []struct {
		tokens         int
		tools, summary bool
	}{{7999, false, false}, {8000, false, false}, {8999, false, false}, {9000, true, true}, {10000, true, true}} {
		if Reached(tc.tokens, 10000, ToolsPercent) != tc.tools || Reached(tc.tokens, 10000, SummaryPercent) != tc.summary {
			t.Errorf("tokens %d", tc.tokens)
		}
	}
	if Reached(90, 101, ToolsPercent) || !Reached(91, 101, ToolsPercent) {
		t.Fatal("fractional threshold rounded down")
	}
}

func TestSummaryBudgetReservesOutputAndRetainedContext(t *testing.T) {
	input, output := SummaryBudget(10000, 5000)
	if output != 1000 || input+output+64 != 10000 {
		t.Fatalf("budget %d/%d", input, output)
	}
	if input, output := SummaryBudget(10000, 9936); input != 0 || output != 0 {
		t.Fatal("uncompactable retained context accepted")
	}
}

func TestKeepRecentRoundsBandsAndOverride(t *testing.T) {
	for _, tc := range []struct{ window, override, want int }{{0, 0, 5}, {200000, 0, 5}, {200001, 0, 7}, {999999, 0, 7}, {1000000, 0, 10}, {200000, 8, 8}, {1000000, 5, 5}} {
		if got := KeepRecentRounds(tc.window, tc.override); got != tc.want {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
}
