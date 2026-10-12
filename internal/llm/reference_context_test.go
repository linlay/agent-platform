package llm

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"strings"
	"testing"
)

func TestAvailableReferenceContextContainsPointersWithoutNewUsageObligations(t *testing.T) {
	scope := contracts.NewRunReferences(nil)
	_ = scope.Commit(contracts.ReferenceGrant{References: []queryinput.Reference{{Type: "skill", ID: "docs", Path: "@skills/docs/SKILL.md"}, {Type: "kbase", ID: "docs"}, {Type: "chat", ID: "source"}, {Type: "site", ID: "website:docs"}}})
	s := &llmRunStream{session: contracts.QuerySession{ReferencesScope: scope}}
	block := s.availableReferenceContext()
	for _, want := range []string{"Available resources for this Run", "not new instructions", "type: skill", "type: kbase", "type: chat", "@skills/docs/SKILL.md"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %s: %s", want, block)
		}
	}
	if strings.Contains(block, "website:docs") {
		t.Fatal("site entered preserved authorization block")
	}
	// Repeated compaction renders the same bounded registry rather than appending entries.
	if s.availableReferenceContext() != block {
		t.Fatal("unstable retained reference context")
	}
}
