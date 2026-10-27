package conversation

import (
	"errors"
	"reflect"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

type primaryTestRuns struct {
	contracts.RunManager
	active map[string]string
}

func (r primaryTestRuns) ActiveRunForChat(chatID string) (contracts.RunStatusInfo, bool, error) {
	if runID, ok := r.active[chatID]; ok {
		return contracts.RunStatusInfo{RunID: runID}, true, nil
	}
	return contracts.RunStatusInfo{}, false, nil
}

type primaryTestSink struct{ events []map[string]any }

func (s *primaryTestSink) Broadcast(eventType string, data map[string]any) {
	if eventType == PrimaryChangedEvent {
		s.events = append(s.events, data)
	}
}

func primaryFixture(t *testing.T) (*Service, *chat.FileStore, primaryTestRuns, *primaryTestSink) {
	t.Helper()
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, id := range []string{"old", "next"} {
		if _, _, err := store.EnsureChatWithSourceAndMode(id, "agent", id, "query:user", "GENERAL"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.EnsureChatWithSourceAndMode("upload", "agent", "", "query:user", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	runs := primaryTestRuns{active: map[string]string{}}
	sink := &primaryTestSink{}
	service := NewService(store, nil, nil, runs)
	service.Notifications = sink
	return service, store, runs, sink
}

func TestSetChatPrimaryRejectsWhileCurrentPrimaryRuns(t *testing.T) {
	service, store, runs, sink := primaryFixture(t)
	if _, err := service.SetChatPrimary("old"); err != nil {
		t.Fatal(err)
	}
	runs.active["old"] = "run-1"

	_, err := service.SetChatPrimary("next")
	var busy *chat.PrimaryChatBusyError
	if !errors.As(err, &busy) || busy.ChatID != "old" || !reflect.DeepEqual(busy.RunIDs, []string{"run-1"}) {
		t.Fatalf("expected busy error, got %v", err)
	}
	if err := service.EnsurePrimaryReplaceable("agent"); !errors.As(err, &busy) {
		t.Fatalf("admission check must also reject: %v", err)
	}
	if id, _ := store.PrimaryChatID("agent"); id != "old" {
		t.Fatalf("rejected switch moved the pointer to %q", id)
	}
	// Re-selecting the running primary is not a switch.
	if result, err := service.SetChatPrimary("old"); err != nil || result.Changed {
		t.Fatalf("same chat: %+v %v", result, err)
	}

	delete(runs.active, "old")
	result, err := service.SetChatPrimary("next")
	if err != nil || !result.Changed || result.PreviousChatID != "old" || result.AgentKey != "agent" {
		t.Fatalf("switch after run ended: %+v %v", result, err)
	}
	want := []map[string]any{
		{"agentKey": "agent", "chatId": "old"},
		{"agentKey": "agent", "chatId": "next", "previousChatId": "old"},
	}
	if !reflect.DeepEqual(sink.events, want) {
		t.Fatalf("events: %v", sink.events)
	}
}

func TestSetChatPrimaryRejectsPendingUploadChat(t *testing.T) {
	service, _, _, sink := primaryFixture(t)
	var validationErr *chat.OrderValidationError
	if _, err := service.SetChatPrimary("upload"); !errors.As(err, &validationErr) {
		t.Fatalf("pending upload chat accepted: %v", err)
	}
	if _, err := service.SetChatPrimary("missing"); !errors.Is(err, chat.ErrChatNotFound) {
		t.Fatalf("missing chat: %v", err)
	}
	// The query path publishes a Chat it just filled, including an upload shell.
	if err := service.PublishPrimaryChat("agent", "upload"); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events: %v", sink.events)
	}
}
