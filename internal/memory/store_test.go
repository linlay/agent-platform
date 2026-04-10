package memory

import (
	"testing"

	"agent-platform-runner-go/internal/api"
)

func TestFileStoreToolQueries(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	assertStoreToolQueries(t, store, "like")
}

func TestSQLiteStoreToolQueries(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir(), "memory.db")
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	assertStoreToolQueries(t, store, "fts")
}

func assertStoreToolQueries(t *testing.T, store Store, expectedMatchType string) {
	t.Helper()

	items := []api.StoredMemoryResponse{
		{
			ID:         "mem-1",
			AgentKey:   "agent-a",
			SubjectKey: "chat:chat-a",
			Summary:    "alpha release note",
			SourceType: "tool-write",
			Category:   "general",
			Importance: 3,
			Tags:       []string{"alpha"},
			CreatedAt:  100,
			UpdatedAt:  100,
		},
		{
			ID:         "mem-2",
			AgentKey:   "agent-a",
			SubjectKey: "chat:chat-b",
			Summary:    "urgent beta bug",
			SourceType: "tool-write",
			Category:   "alerts",
			Importance: 9,
			Tags:       []string{"beta", "urgent"},
			CreatedAt:  200,
			UpdatedAt:  200,
		},
		{
			ID:         "mem-3",
			AgentKey:   "agent-b",
			SubjectKey: "chat:chat-c",
			Summary:    "other agent memo",
			SourceType: "tool-write",
			Category:   "general",
			Importance: 10,
			Tags:       []string{"other"},
			CreatedAt:  300,
			UpdatedAt:  300,
		},
	}
	for _, item := range items {
		if err := store.Write(item); err != nil {
			t.Fatalf("write memory %s: %v", item.ID, err)
		}
	}

	t.Run("ListRecentFiltersByAgentAndCategory", func(t *testing.T) {
		results, err := store.List("agent-a", "alerts", 10, "recent")
		if err != nil {
			t.Fatalf("list memories: %v", err)
		}
		if len(results) != 1 || results[0].ID != "mem-2" {
			t.Fatalf("expected only alerts memory for agent-a, got %#v", results)
		}
	})

	t.Run("ListImportanceSort", func(t *testing.T) {
		results, err := store.List("agent-a", "", 10, "importance")
		if err != nil {
			t.Fatalf("list memories by importance: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 memories for agent-a, got %#v", results)
		}
		if results[0].ID != "mem-2" || results[1].ID != "mem-1" {
			t.Fatalf("expected importance sort mem-2 then mem-1, got %#v", results)
		}
	})

	t.Run("ReadDetailRespectsAgentFilter", func(t *testing.T) {
		record, err := store.ReadDetail("agent-a", "mem-2")
		if err != nil {
			t.Fatalf("read detail: %v", err)
		}
		if record == nil || record.ID != "mem-2" || record.Content != "urgent beta bug" {
			t.Fatalf("expected mem-2 detail, got %#v", record)
		}
		if record.SubjectKey != "chat:chat-b" {
			t.Fatalf("expected subjectKey chat:chat-b, got %#v", record)
		}

		missing, err := store.ReadDetail("agent-a", "mem-3")
		if err != nil {
			t.Fatalf("read detail with mismatched agent: %v", err)
		}
		if missing != nil {
			t.Fatalf("expected agent filter to hide mem-3, got %#v", missing)
		}
	})

	t.Run("SearchDetailedReturnsScoreAndMatchType", func(t *testing.T) {
		results, err := store.SearchDetailed("agent-a", "beta", "alerts", 10)
		if err != nil {
			t.Fatalf("search detailed: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 beta result, got %#v", results)
		}
		if results[0].Memory.ID != "mem-2" {
			t.Fatalf("expected mem-2 search result, got %#v", results[0])
		}
		if results[0].MatchType != expectedMatchType && !(expectedMatchType == "fts" && results[0].MatchType == "like") {
			t.Fatalf("expected matchType %s (or like fallback), got %#v", expectedMatchType, results[0])
		}
		if results[0].Score < 0 {
			t.Fatalf("expected non-negative score, got %#v", results[0])
		}
	})
}
