package skills

import "testing"

func TestFileCandidateStoreWriteAndList(t *testing.T) {
	store, err := NewFileCandidateStore(t.TempDir())
	if err != nil {
		t.Fatalf("new candidate store: %v", err)
	}
	created, err := store.Write(CandidateInput{
		AgentKey:   "agent-a",
		ChatID:     "chat-1",
		RunID:      "run-1",
		SourceKind: "learn",
		Title:      "Rollback workflow",
		Summary:    "Run the rollback checklist before redeploy.",
		Procedure:  "First verify health checks, then rollback deployment, then clear cache.",
		Category:   "workflow",
		Confidence: 0.8,
		Tags:       []string{"procedure", "rollback"},
	})
	if err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected generated id, got %#v", created)
	}
	items, err := store.List("agent-a", 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Rollback workflow" {
		t.Fatalf("unexpected candidates: %#v", items)
	}
}
