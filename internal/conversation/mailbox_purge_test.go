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

func TestDeleteArchiveAlsoPurgesItsMail(t *testing.T) {
	root := t.TempDir()
	active, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	archive, err := chat.NewArchiveStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := active.EnsureChatWithSourceAndMode("archived-chat", "agent", "hello", "query:alice", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	controlAppendRun(t, active, "archived-chat", "run-archived", "finished")
	s := NewService(active, archive, chat.NewArchiver(active, archive), nil)
	if _, err := s.ArchiveChats([]string{"archived-chat"}); err != nil {
		t.Fatal(err)
	}
	if exists, err := archive.HasChat("archived-chat"); err != nil || !exists {
		t.Fatal(exists, err)
	}
	called := ""
	s.MailboxPurge = func(id string) error { called = id; return errors.New("mail temporarily busy") }
	if err := s.DeleteArchive("archived-chat"); err != nil {
		t.Fatal(err)
	}
	if called != "archived-chat" {
		t.Fatal("mail purge hook missing")
	}
	if exists, err := archive.HasChat("archived-chat"); err != nil || exists {
		t.Fatal(exists, err)
	}
}
