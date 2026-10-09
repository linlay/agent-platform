package adminsource

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/kbasescenter"
	"context"
	"testing"
)

func TestPrepareKnowledgeBindingRollsBackOnlyNewLibrary(t *testing.T) {
	center, err := kbasescenter.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer center.Close(context.Background())
	service := &Service{}
	for _, commit := range []bool{false, true} {
		def, finish, err := service.PrepareKnowledgeBinding(center, map[string]any{"mode": "GENERAL"}, "docs", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		id := contracts.AnyMapNode(def["kbaseConfig"])["libraryId"].(string)
		if err = finish(commit); err != nil {
			t.Fatal(err)
		}
		_, err = center.Get(id)
		if commit && err != nil || !commit && err == nil {
			t.Fatalf("commit %v: %v", commit, err)
		}
	}
}
