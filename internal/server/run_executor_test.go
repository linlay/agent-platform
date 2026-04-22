package server

import (
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	. "agent-platform-runner-go/internal/contracts"
)

func TestPersistRunCompletionInvokesOnPersisted(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "team-1", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	var seen chat.RunCompletion
	called := false
	persistRunCompletionIfNeeded(RunExecutorParams{
		Request: api.QueryRequest{
			ChatID:   "chat-1",
			RunID:    "run-1",
			Message:  "hello",
			AgentKey: "agent-a",
			TeamID:   "team-1",
		},
		Session: QuerySession{
			ChatID:   "chat-1",
			RunID:    "run-1",
			AgentKey: "agent-a",
			TeamID:   "team-1",
		},
		Chats: chats,
		OnPersisted: func(completion chat.RunCompletion) {
			called = true
			seen = completion
		},
	}, "assistant reply", chat.UsageData{}, true)

	if !called {
		t.Fatalf("expected OnPersisted callback")
	}
	if seen.ChatID != "chat-1" || seen.RunID != "run-1" || seen.AssistantText != "assistant reply" {
		t.Fatalf("unexpected completion callback payload: %#v", seen)
	}
}

func TestPersistRunCompletionSkipsOnPersistedWhenNotSuccessful(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "team-1", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	called := false
	persistRunCompletionIfNeeded(RunExecutorParams{
		Request: api.QueryRequest{
			ChatID:   "chat-1",
			RunID:    "run-1",
			Message:  "hello",
			AgentKey: "agent-a",
			TeamID:   "team-1",
		},
		Session: QuerySession{
			ChatID:   "chat-1",
			RunID:    "run-1",
			AgentKey: "agent-a",
			TeamID:   "team-1",
		},
		Chats: chats,
		OnPersisted: func(completion chat.RunCompletion) {
			called = true
		},
	}, "partial assistant reply", chat.UsageData{TotalTokens: 10}, false)

	if called {
		t.Fatal("did not expect OnPersisted callback for unsuccessful completion")
	}
}

func TestPersistRunCompletionInvokesOnUnreadChanged(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "team-1", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	var seen chat.Summary
	called := false
	persistRunCompletionIfNeeded(RunExecutorParams{
		Request: api.QueryRequest{
			ChatID:   "chat-1",
			RunID:    "run-1",
			Message:  "hello",
			AgentKey: "agent-a",
			TeamID:   "team-1",
		},
		Session: QuerySession{
			ChatID:   "chat-1",
			RunID:    "run-1",
			AgentKey: "agent-a",
			TeamID:   "team-1",
		},
		Chats: chats,
		OnUnreadChanged: func(summary chat.Summary) {
			called = true
			seen = summary
		},
	}, "assistant reply", chat.UsageData{}, true)

	if !called {
		t.Fatalf("expected OnUnreadChanged callback")
	}
	if seen.ChatID != "chat-1" || seen.LastRunID != "run-1" || seen.Read.IsRead {
		t.Fatalf("unexpected unread callback payload: %#v", seen)
	}
}
