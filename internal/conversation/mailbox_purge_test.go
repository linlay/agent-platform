package conversation

import (
	"errors"
	"testing"

	"agent-platform/internal/chat"
)

func TestDeleteChatSucceedsWhenMailCleanupFails(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.EnsureChatWithSourceAndMode("c1", "agent", "hello", "", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	purged := ""
	s := &Service{Chats: store, MailboxPurge: func(id string) error { purged = id; return errors.New("mail.db busy") }}
	if err := s.DeleteChat("c1"); err != nil {
		t.Fatalf("mail cleanup failure surfaced as a delete failure: %v", err)
	}
	if purged != "c1" {
		t.Fatalf("mail cleanup not attempted: %q", purged)
	}
	if summary, err := store.Summary("c1"); err != nil || summary != nil {
		t.Fatalf("chat not deleted: %#v %v", summary, err)
	}
}
