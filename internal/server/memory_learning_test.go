package server

import (
	"testing"

	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

func TestAutoLearnIfEnabledStoresObservation(t *testing.T) {
	chats, memories := seedAutoLearnTestStores(t)
	s := &Server{
		deps: Dependencies{
			Config: config.Config{
				Memory: config.MemoryConfig{
					AutoRememberEnabled: true,
				},
			},
			Chats:  chats,
			Memory: memories,
		},
	}

	s.autoLearnIfEnabled("chat-auto", "run-auto", "agent-a", "team-1", &Principal{Subject: "user-1"}, "req-auto")

	results, err := memories.SearchDetailed("agent-a", "memory bug", "", 10)
	if err != nil {
		t.Fatalf("search detailed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected learned observation, got %#v", results)
	}
	if results[0].Memory.Kind != memory.KindObservation {
		t.Fatalf("expected observation kind, got %#v", results[0].Memory)
	}
}

func TestAutoLearnIfEnabledRespectsConfigFlag(t *testing.T) {
	chats, memories := seedAutoLearnTestStores(t)
	s := &Server{
		deps: Dependencies{
			Config: config.Config{
				Memory: config.MemoryConfig{
					AutoRememberEnabled: false,
				},
			},
			Chats:  chats,
			Memory: memories,
		},
	}

	s.autoLearnIfEnabled("chat-auto", "run-auto", "agent-a", "team-1", &Principal{Subject: "user-1"}, "req-auto")

	results, err := memories.SearchDetailed("agent-a", "memory bug", "", 10)
	if err != nil {
		t.Fatalf("search detailed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no learned memory when auto learn disabled, got %#v", results)
	}
}

func seedAutoLearnTestStores(t *testing.T) (*chat.FileStore, *memory.FileStore) {
	t.Helper()
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	memories, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-auto", "agent-a", "team-1", "please fix the memory bug"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-auto", chat.QueryLine{
		ChatID:    "chat-auto",
		RunID:     "run-auto",
		UpdatedAt: 100,
		Query: map[string]any{
			"message": "please fix the memory bug",
			"role":    "user",
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}
	if err := chats.AppendStepLine("chat-auto", chat.StepLine{
		ChatID:    "chat-auto",
		RunID:     "run-auto",
		UpdatedAt: 200,
		Type:      "react",
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: "Fixed the memory bug and tightened the retrieval scope."}},
			},
		},
	}); err != nil {
		t.Fatalf("append step line: %v", err)
	}
	if err := chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          "chat-auto",
		RunID:           "run-auto",
		AssistantText:   "Fixed the memory bug and tightened the retrieval scope.",
		UpdatedAtMillis: 300,
	}); err != nil {
		t.Fatalf("on run completed: %v", err)
	}
	return chats, memories
}
