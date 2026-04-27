package chat

import "testing"

func TestSearchSessionFindsQueryMessageAndEvent(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat-1", "agent-a", "", "Need deploy rollback notes"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendQueryLine("chat-1", QueryLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 100,
		Query: map[string]any{
			"message": "Need deploy rollback notes",
			"role":    "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine("chat-1", StepLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 200,
		Type:      "react",
		Stage:     "execute",
		Messages: []StoredMessage{
			{
				Role:    "assistant",
				Content: []ContentPart{{Type: "text", Text: "Rollback notes: revert deployment and clear cache."}},
				Ts:      int64Ptr(210),
			},
		},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	if err := store.AppendEventLine("chat-1", EventLine{
		ChatID:    "chat-1",
		RunID:     "run-1",
		UpdatedAt: 300,
		Type:      "event",
		Event: map[string]any{
			"type": "source.publish",
			"text": "Deployment rollback checklist published",
		},
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	hits, err := store.SearchSession("chat-1", "rollback", 10)
	if err != nil {
		t.Fatalf("search session: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected at least 2 hits, got %#v", hits)
	}
	if hits[0].Snippet == "" {
		t.Fatalf("expected snippet on top hit, got %#v", hits[0])
	}
	foundQuery := false
	foundMessage := false
	for _, hit := range hits {
		if hit.Kind == "query" && hit.Role == "user" {
			foundQuery = true
		}
		if hit.Kind == "message" && hit.Role == "assistant" && hit.Stage == "execute" {
			foundMessage = true
		}
	}
	if !foundQuery || !foundMessage {
		t.Fatalf("expected query and message hits, got %#v", hits)
	}
}

func TestSearchGlobalFiltersAgentAndIncludesChatMetadata(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	for _, item := range []struct {
		chatID   string
		agentKey string
		teamID   string
		message  string
	}{
		{"chat-a", "agent-a", "team-a", "rollback deploy"},
		{"chat-b", "agent-b", "team-b", "rollback billing"},
	} {
		if _, _, err := store.EnsureChat(item.chatID, item.agentKey, item.teamID, item.message); err != nil {
			t.Fatalf("ensure %s: %v", item.chatID, err)
		}
		if err := store.AppendQueryLine(item.chatID, QueryLine{
			ChatID:    item.chatID,
			RunID:     item.chatID + "-run",
			UpdatedAt: 100,
			Query:     map[string]any{"message": item.message, "role": "user"},
			Type:      "query",
		}); err != nil {
			t.Fatalf("append query %s: %v", item.chatID, err)
		}
		if err := store.OnRunCompleted(RunCompletion{ChatID: item.chatID, RunID: item.chatID + "-run", UpdatedAtMillis: 100}); err != nil {
			t.Fatalf("complete %s: %v", item.chatID, err)
		}
	}

	hits, err := store.SearchGlobal("rollback", "agent-a", "", 20)
	if err != nil {
		t.Fatalf("search global: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected one agent-a hit, got %#v", hits)
	}
	if hits[0].ChatID != "chat-a" || hits[0].ChatName == "" || hits[0].AgentKey != "agent-a" {
		t.Fatalf("expected chat metadata on hit, got %#v", hits[0])
	}
	hits, err = store.SearchGlobal("rollback", "", "team-b", 20)
	if err != nil {
		t.Fatalf("search global by team: %v", err)
	}
	if len(hits) != 1 || hits[0].ChatID != "chat-b" || hits[0].TeamID != "team-b" {
		t.Fatalf("expected one team-b hit with team metadata, got %#v", hits)
	}
}

func int64Ptr(v int64) *int64 { return &v }
