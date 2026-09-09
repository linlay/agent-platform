package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestChatPinsIndependentOrderAndFiltersBeforeLimit(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("chat-%02d", i)
		mode := "REACT"
		if i == 0 {
			mode = "KBASE"
		}
		if i == 1 {
			mode = "CODER"
		}
		if _, _, err := s.EnsureChatWithSourceAndMode(id, "agent-a", "", id, "", mode); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE CHATS SET UPDATED_AT_=? WHERE CHAT_ID_=?", int64(1_780_000_000_000+i), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.MoveChat("chat-02", "chat-59", ""); err != nil {
		t.Fatal(err)
	}
	manual, _ := os.ReadFile(s.chatOrderPath())
	before, _ := s.Summary("chat-00")
	for _, id := range []string{"chat-00", "chat-01", "chat-59", "chat-58"} {
		if _, changed, err := s.SetChatPinned(id, true); err != nil || !changed {
			t.Fatalf("pin %s: %v %v", id, changed, err)
		}
	}
	pins, _ := s.ChatPinned()
	retry, changed, err := s.SetChatPinned("chat-00", true)
	if err != nil || changed || !reflect.DeepEqual(retry, pins) {
		t.Fatalf("non-idempotent pin: %+v %v %v", retry, changed, err)
	}
	if _, err := s.MoveChat("chat-00", "chat-58", ""); err != nil {
		t.Fatal(err)
	}
	afterManual, _ := os.ReadFile(s.chatOrderPath())
	if string(manual) != string(afterManual) {
		t.Fatal("pinning changed manual order")
	}
	after, _ := s.Summary("chat-00")
	if !after.Pinned || before.UpdatedAt != after.UpdatedAt || !reflect.DeepEqual(before.Read, after.Read) {
		t.Fatal("pinning changed content/read state")
	}
	if _, err := s.MoveChat("chat-00", "chat-02", ""); err == nil {
		t.Fatal("cross-group move succeeded")
	}
	yes, no := true, false
	items, err := s.ListChatsWithOptions(ListOptions{Pinned: &yes})
	if err != nil || !reflect.DeepEqual(summaryIDs(items), []string{"chat-00", "chat-58", "chat-59", "chat-01"}) {
		t.Fatalf("pins: %v %v", summaryIDs(items), err)
	}
	items, err = s.ListChatsWithOptions(ListOptions{AgentModes: []string{"REACT"}, Pinned: &no, Limit: 8})
	if err != nil || len(items) != 8 || items[0].ChatID != "chat-02" {
		t.Fatalf("filtered manual list: %v %v", summaryIDs(items), err)
	}
	for _, item := range items {
		if item.Pinned {
			t.Fatal("pinned item consumed ordinary limit")
		}
	}
	items, err = s.RecentChatsByOwner("agent-a", "", 50, &no)
	if err != nil || len(items) != 50 || items[0].ChatID != "chat-57" {
		t.Fatalf("owner preview: %v %v", summaryIDs(items), err)
	}
	if _, _, err := s.SetChatPinned("chat-00", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reloaded, err := s.ChatPinned()
	if err != nil || !reflect.DeepEqual(reloaded.Order, []string{"chat-58", "chat-59", "chat-01"}) {
		t.Fatalf("restart pins: %+v %v", reloaded, err)
	}
}

func TestChatPinsConcurrentWritesAndLifecycle(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("chat-%02d", i)
		if _, _, err := s.EnsureChat(id, "agent", "", id); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.SetChatPinned(id, true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	pins, err := s.ChatPinned()
	if err != nil || len(pins.Order) != 16 {
		t.Fatalf("lost pins: %+v %v", pins, err)
	}
	if err := s.DeleteChat("chat-00"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureChat("chat-00", "agent", "", "recreated"); err != nil {
		t.Fatal(err)
	}
	summary, _ := s.Summary("chat-00")
	if summary.Pinned {
		t.Fatal("deleted pin revived")
	}
	if _, _, err := s.SetChatPinned("missing", true); err != ErrChatNotFound {
		t.Fatalf("missing pin: %v", err)
	}
	if _, _, err := s.SetChatPinned("missing", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureChat("pending", "agent", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetChatPinned("pending", true); err == nil {
		t.Fatal("pinned pending upload")
	}
	path := filepath.Join(s.root, ChatPinnedFileName)
	if err := os.WriteFile(path, []byte("{damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetChatPinned("chat-00", true); err == nil {
		t.Fatal("overwrote damaged pins")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{damaged" {
		t.Fatal("damaged pins changed")
	}
}

func TestChatPinsArchiveRestoreClearsResidualPreferences(t *testing.T) {
	root := t.TempDir()
	active, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	archive, err := NewArchiveStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := active.EnsureChat("chat-archive-pin", "agent", "", "archive"); err != nil {
		t.Fatal(err)
	}
	if err := completeRunForTest(active, RunCompletion{
		ChatID:          "chat-archive-pin",
		RunID:           "run-archive-pin",
		AgentKey:        "agent",
		AssistantText:   "archived response",
		InitialMessage:  "archive",
		FinishReason:    "complete",
		StartedAtMillis: testEpochMillis(1000),
		UpdatedAtMillis: testEpochMillis(2000),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := active.SetChatPinned("chat-archive-pin", true); err != nil {
		t.Fatal(err)
	}
	archiver := NewArchiver(active, archive)
	if err := archiver.ArchiveChat("chat-archive-pin"); err != nil {
		t.Fatal(err)
	}
	pins, err := active.ChatPinned()
	if err != nil || len(pins.Order) != 0 {
		t.Fatalf("archive pins: %+v %v", pins, err)
	}
	// Simulate an old residual preference surviving a previous process.
	if err := os.WriteFile(filepath.Join(root, ChatPinnedFileName), []byte(`{"version":1,"order":["chat-archive-pin"],"updatedAt":1780000000000}`), 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := archiver.RestoreChat("chat-archive-pin")
	if err != nil || summary.Pinned {
		t.Fatalf("restore: %+v %v", summary, err)
	}
	pins, err = active.ChatPinned()
	if err != nil || len(pins.Order) != 0 {
		t.Fatalf("restored pins: %+v %v", pins, err)
	}
}
