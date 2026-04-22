package server

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/stream"
)

type recordingNotificationSink struct {
	mu         sync.Mutex
	eventTypes []string
}

func (s *recordingNotificationSink) Broadcast(eventType string, _ map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventTypes = append(s.eventTypes, eventType)
}

func (s *recordingNotificationSink) EventTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.eventTypes...)
}

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
	persisted, completion := persistRunCompletionIfNeeded(RunExecutorParams{
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

	if !persisted {
		t.Fatalf("expected completion to persist")
	}
	if !called {
		t.Fatalf("expected OnPersisted callback")
	}
	if completion.ChatID != "chat-1" || completion.RunID != "run-1" || completion.AssistantText != "assistant reply" {
		t.Fatalf("unexpected persisted completion: %#v", completion)
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
	persisted, completion := persistRunCompletionIfNeeded(RunExecutorParams{
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

	if !persisted {
		t.Fatal("expected partial completion to persist")
	}
	if completion.RunID != "run-1" {
		t.Fatalf("unexpected completion payload: %#v", completion)
	}
	if called {
		t.Fatal("did not expect OnPersisted callback for unsuccessful completion")
	}
}

func TestBroadcastRunCompletionEmitsUnreadBeforeChatUpdated(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "team-1", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	persisted, completion := persistRunCompletionIfNeeded(RunExecutorParams{
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
	}, "assistant reply", chat.UsageData{}, true)

	if !persisted {
		t.Fatalf("expected completion to persist")
	}

	var order []string
	notifications := &recordingNotificationSink{}
	broadcastRunCompletion(RunExecutorParams{
		Chats: chats,
		OnUnreadChanged: func(summary chat.Summary) {
			order = append(order, "chat.unread")
			if summary.ChatID != "chat-1" || summary.LastRunID != "run-1" || summary.Read.IsRead {
				t.Fatalf("unexpected unread callback payload: %#v", summary)
			}
		},
		Notifications: notifications,
	}, completion)

	order = append(order, notifications.EventTypes()...)
	if want := []string{"chat.unread", "chat.updated"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("unexpected broadcast order: got %v want %v", order, want)
	}
}

func TestRunExecutorFinalizesAfterStreamDrain(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "team-1", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	eventBus := stream.NewRunEventBus(32, 0, nil)
	observer, err := eventBus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe observer: %v", err)
	}

	var (
		mu    sync.Mutex
		order []string
	)
	drainDone := make(chan struct{})
	go func() {
		for range observer.Events {
		}
		mu.Lock()
		order = append(order, "stream.done")
		mu.Unlock()
		observer.MarkDone()
		close(drainDone)
	}()

	notifications := &recordingNotificationSink{}
	agent := &orchestratorAgentEngine{
		streams: []AgentStream{
			&stubOrchestratableStream{
				deltas: []AgentDelta{
					DeltaContent{Text: "hello"},
				},
			},
		},
	}
	runExecutor(RunExecutorParams{
		RunCtx:  context.Background(),
		Request: api.QueryRequest{ChatID: "chat-1", RunID: "run-1", Message: "hello", AgentKey: "agent-a", TeamID: "team-1"},
		Session: QuerySession{ChatID: "chat-1", RunID: "run-1", AgentKey: "agent-a", TeamID: "team-1"},
		Summary: chat.Summary{ChatID: "chat-1", AgentKey: "agent-a"},
		Agent:   agent,
		Assembler: stream.NewAssembler(stream.StreamRequest{
			RunID:    "run-1",
			ChatID:   "chat-1",
			AgentKey: "agent-a",
			Message:  "hello",
		}),
		Mapper:        llm.NewDeltaMapper("run-1", "chat-1", 0, nil, nil),
		EventBus:      eventBus,
		Chats:         chats,
		Notifications: notifications,
		OnUnreadChanged: func(chat.Summary) {
			mu.Lock()
			order = append(order, "chat.unread")
			mu.Unlock()
		},
		OnComplete: func(string) {
			mu.Lock()
			order = append(order, "run.finished")
			mu.Unlock()
		},
	})

	<-drainDone
	mu.Lock()
	order = append(order, notifications.EventTypes()...)
	got := append([]string(nil), order...)
	mu.Unlock()

	if want := []string{"stream.done", "run.finished", "chat.unread", "chat.updated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected finalization order: got %v want %v", got, want)
	}
}
