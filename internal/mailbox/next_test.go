package mailbox

import (
	"agent-platform/internal/runtime/controlscope"
	"errors"
	"testing"
	"time"
)

func TestOpenedChatsOnlyExplicitNewChatAndSubject(t *testing.T) {
	s, _ := testBox(t)
	for _, item := range []struct {
		id, subject string
		newChat     bool
	}{{"work", "user", true}, {"primary", "user", false}, {"other", "other", true}} {
		m := sample(item.id, Letter, Done)
		m.OpenedChat = true
		m.NewChat = item.newChat
		m.Subject = item.subject
		put(t, s.Store, m)
	}
	rows, err := s.Store.OpenedChats("lead", "user")
	if err != nil || len(rows) != 1 || rows[0].ID != "work" {
		t.Fatal(rows, err)
	}
	if _, err := s.Store.Change("work", func(m *Mail) error { m.ToChatClosedAt = now(); return nil }); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Store.OpenedChats("lead", "user")
	if err != nil || len(rows) != 1 {
		t.Fatal("closing must retain creation relation", rows, err)
	}
}

func TestRetryHasOneTimerPerChatAndStopsOnClose(t *testing.T) {
	s, _ := testBox(t)
	s.retry("chat", errors.New("temporary"))
	s.mu.Lock()
	first := s.retries["chat"]
	delay := s.retryDelay["chat"]
	s.mu.Unlock()
	s.retry("chat", errors.New("temporary"))
	s.mu.Lock()
	same := s.retries["chat"] == first
	s.mu.Unlock()
	if first == nil || !same || delay != 10*time.Second {
		t.Fatal("retry not coalesced", same, delay)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if first.Stop() {
		t.Fatal("close did not stop timer")
	}
}

func TestNormalQueueingResetsTemporaryFailureBackoff(t *testing.T) {
	s, p := testBox(t)
	p.target = Target{Queue: true}
	m := sample("queue", Letter, Queued)
	put(t, s.Store, m)
	s.setScope(m.ID, controlscope.Scope{})
	s.retry("receiver", errors.New("transient"))
	if err := s.dispatch("receiver"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retryDelay["receiver"] != 0 || s.retries["receiver"] != nil {
		t.Fatal("normal queueing retained backoff")
	}
}
