package orchestration

import (
	"testing"

	runtimetypes "agent-platform/internal/runtime/types"
)

func TestDeduplicateReferencesUsesStableIdentityAndOrder(t *testing.T) {
	got := DeduplicateReferences([]runtimetypes.Reference{
		{ID: "one", Name: "first"},
		{ID: "one", Name: "duplicate"},
		{SHA256: "abc", Name: "second"},
	})
	if len(got) != 2 || got[0].Name != "first" || got[1].Name != "second" {
		t.Fatalf("deduplicated = %#v", got)
	}
}
