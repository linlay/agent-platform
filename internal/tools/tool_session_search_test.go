package tools

import (
	"context"
	"testing"

	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
)

func TestSessionSearchToolUsesCurrentChatByDefault(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "", "Need rollback notes"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-1", chat.QueryLine{
		ChatID: "chat-1", RunID: "run-1", UpdatedAt: 100, Type: "query",
		Query: map[string]any{"message": "Need rollback notes", "role": "user"},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}

	executor, err := NewRuntimeToolExecutor(config.Config{}, nil, chats, nil, nil)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}
	result, err := executor.Invoke(context.Background(), "_session_search_", map[string]any{
		"query": "rollback",
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-1"}})
	if err != nil {
		t.Fatalf("invoke session search: %v", err)
	}
	if result.Error != "" || result.ExitCode != 0 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if result.Structured["count"].(int) != 1 {
		t.Fatalf("expected one hit, got %#v", result.Structured)
	}
}
