package terminal

import (
	"testing"

	"agent-platform/internal/timecontract"
)

func TestListStrictRejectsSessionWithoutStartedAt(t *testing.T) {
	manager := NewManager()
	manager.sessions["term_invalid"] = &Session{
		id:       "term_invalid",
		ownerKey: "owner-a",
		agentKey: "agent-a",
	}
	if _, err := manager.ListStrict("owner-a"); !timecontract.IsViolation(err) {
		t.Fatalf("expected invalid terminal startedAt violation, got %v", err)
	}
	if infos := manager.List("owner-a"); len(infos) != 0 {
		t.Fatalf("non-strict internal listing must not expose zero startedAt: %#v", infos)
	}
}
