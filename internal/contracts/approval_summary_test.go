package contracts

import (
	"strings"
	"testing"
)

func TestApprovalSummaryBoundsAndClone(t *testing.T) {
	items := []any{}
	for range 4 {
		items = append(items, map[string]any{"toolName": "bash", "description": strings.Repeat("中", 250), "requirements": []any{map[string]any{"reason": "policy reason"}}, "command": "do not expose"})
	}
	summaries, truncated := SummarizeApprovals(items)
	if len(summaries) != 3 || !truncated || len([]rune(summaries[0].Description)) != 200 || summaries[0].Reason != "policy reason" {
		t.Fatalf("%#v %v", summaries, truncated)
	}
	original := AwaitingSubmitContext{Summaries: summaries, SummariesTruncated: truncated}
	clone := original.Clone()
	clone.Summaries[0].Reason = "changed"
	if original.Summaries[0].Reason != "policy reason" {
		t.Fatal("clone aliases summaries")
	}
}
