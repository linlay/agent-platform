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

func int64Ptr(v int64) *int64 { return &v }
