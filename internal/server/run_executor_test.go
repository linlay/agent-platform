package server

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/stream"
)

type recordingNotificationSink struct {
	mu         sync.Mutex
	eventTypes []string
	payloads   []map[string]any
}

func (s *recordingNotificationSink) Broadcast(eventType string, payload map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventTypes = append(s.eventTypes, eventType)
	cloned := map[string]any{}
	for key, value := range payload {
		cloned[key] = value
	}
	s.payloads = append(s.payloads, cloned)
}

func (s *recordingNotificationSink) EventTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.eventTypes...)
}

func (s *recordingNotificationSink) Payloads() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.payloads...)
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
	persisted, completion := persistRunCompletionWithReason(RunExecutorParams{
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
	}, "assistant reply", chat.UsageData{}, "complete", true)

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
	persisted, completion := persistRunCompletionWithReason(RunExecutorParams{
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
	}, "partial assistant reply", chat.UsageData{TotalTokens: 10}, "error", false)

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

	persisted, completion := persistRunCompletionWithReason(RunExecutorParams{
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
	}, "assistant reply", chat.UsageData{}, "complete", true)

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

func TestHandleAwaitingLifecycleBroadcastsViewportMetadata(t *testing.T) {
	notifications := &recordingNotificationSink{}
	tracker := &awaitingTracker{}
	handleAwaitingLifecycle(RunExecutorParams{
		Session: QuerySession{
			ChatID:   "chat-1",
			RunID:    "run-1",
			AgentKey: "agent-a",
		},
		Notifications: notifications,
	}, stream.EventData{
		Type:      "awaiting.ask",
		Timestamp: 1234,
		Payload: map[string]any{
			"awaitingId":   "await-1",
			"runId":        "run-1",
			"mode":         "form",
			"timeout":      120,
			"viewportType": "html",
			"viewportKey":  "leave_form",
		},
	}, tracker)

	if eventTypes := notifications.EventTypes(); !reflect.DeepEqual(eventTypes, []string{"awaiting.asking"}) {
		t.Fatalf("unexpected event types: %#v", eventTypes)
	}
	payloads := notifications.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("expected one notification payload, got %#v", payloads)
	}
	payload := payloads[0]
	if payload["viewportType"] != "html" || payload["viewportKey"] != "leave_form" {
		t.Fatalf("expected viewport metadata in awaiting.asking notification, got %#v", payload)
	}
}

func TestHandleAwaitingLifecycleBroadcastsAwaitAskPushForApprovalAndPlan(t *testing.T) {
	testCases := []struct {
		mode          string
		awaitingID    string
		timeout       int
		expectTimeout bool
	}{
		{mode: "approval", awaitingID: "await-approval", timeout: 600, expectTimeout: true},
		{mode: "plan", awaitingID: "await-plan"},
	}

	for _, tc := range testCases {
		t.Run(tc.mode, func(t *testing.T) {
			notifications := &recordingNotificationSink{}
			tracker := &awaitingTracker{}
			eventPayload := map[string]any{
				"awaitingId": tc.awaitingID,
				"runId":      "run-1",
				"mode":       tc.mode,
			}
			if tc.expectTimeout {
				eventPayload["timeout"] = tc.timeout
			}
			handleAwaitingLifecycle(RunExecutorParams{
				Session: QuerySession{
					ChatID:   "chat-1",
					RunID:    "run-1",
					AgentKey: "agent-a",
				},
				Notifications: notifications,
			}, stream.EventData{
				Type:      "awaiting.ask",
				Timestamp: 1234,
				Payload:   eventPayload,
			}, tracker)

			if eventTypes := notifications.EventTypes(); !reflect.DeepEqual(eventTypes, []string{"awaiting.asking"}) {
				t.Fatalf("unexpected event types: %#v", eventTypes)
			}
			payloads := notifications.Payloads()
			if len(payloads) != 1 {
				t.Fatalf("expected one notification payload, got %#v", payloads)
			}
			payload := payloads[0]
			if payload["chatId"] != "chat-1" || payload["runId"] != "run-1" || payload["agentKey"] != "agent-a" {
				t.Fatalf("unexpected awaiting.asking identity payload %#v", payload)
			}
			if payload["awaitingId"] != tc.awaitingID || payload["mode"] != tc.mode || payload["createdAt"] != int64(1234) {
				t.Fatalf("unexpected awaiting.asking payload %#v", payload)
			}
			if _, exists := payload["timeout"]; exists != tc.expectTimeout {
				t.Fatalf("unexpected awaiting.asking timeout payload %#v", payload)
			}
			if tc.expectTimeout && payload["timeout"] != tc.timeout {
				t.Fatalf("unexpected awaiting.asking timeout %#v", payload)
			}
		})
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
		Mapper:        llm.NewDeltaMapper("run-1", "chat-1", Budget{}, nil, nil),
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
