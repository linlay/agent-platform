package compaction

import "testing"

func TestThresholdBoundaries(t *testing.T) {
	for _, tc := range []struct {
		tokens         int
		tools, summary bool
	}{{7999, false, false}, {8000, true, false}, {8999, true, false}, {9000, true, true}, {10000, true, true}} {
		if Reached(tc.tokens, 10000, ToolsPercent) != tc.tools || Reached(tc.tokens, 10000, SummaryPercent) != tc.summary {
			t.Errorf("tokens %d", tc.tokens)
		}
	}
	if Reached(80, 101, ToolsPercent) || !Reached(81, 101, ToolsPercent) {
		t.Fatal("fractional threshold rounded down")
	}
}

func TestSummaryBudgetReservesOutputAndRetainedContext(t *testing.T) {
	input, output := SummaryBudget(10000, 5000)
	if output != 936 || input+output+64 != 10000 {
		t.Fatalf("budget %d/%d", input, output)
	}
	if input, output := SummaryBudget(10000, 6000); input != 0 || output != 0 {
		t.Fatal("uncompactable retained context accepted")
	}
}
