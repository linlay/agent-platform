package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestChatOrderMoveFromRecentAppliesBeforeLimitAndRestoresManual(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for index := 0; index < 20; index++ {
		id := fmt.Sprintf("chat-%02d", index)
		if _, _, err := store.EnsureChatWithSourceAndMode(id, "agent-a", "", id, "", "REACT"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec("UPDATE CHATS SET UPDATED_AT_=? WHERE CHAT_ID_=?", int64(1_780_000_000_000+index), id); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := store.ListChatsWithAgentModesAndLimit("", "", []string{"REACT"}, 16)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryIDs(recent); got[0] != "chat-19" || len(got) != 16 {
		t.Fatalf("recent IDs = %v", got)
	}
	state, err := store.MoveChat("chat-00", "chat-19", "")
	if err != nil {
		t.Fatal(err)
	}
	if state.SortMode != SortModeManual {
		t.Fatalf("sort mode = %q, want manual", state.SortMode)
	}
	manual, err := store.ListChatsWithAgentModesAndLimit("", "", []string{"REACT"}, 16)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryIDs(manual); got[0] != "chat-00" || len(got) != 16 {
		t.Fatalf("manual IDs = %v", got)
	}

	if _, err := store.SetChatSortMode(SortModeRecent); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListChatsWithAgentModesAndLimit("", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryIDs(items)[0]; got != "chat-19" {
		t.Fatalf("recent first = %s", got)
	}
	if _, err := store.SetChatSortMode(SortModeManual); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListChatsWithAgentModesAndLimit("", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryIDs(items)[0]; got != "chat-00" {
		t.Fatalf("restored manual first = %s", got)
	}
}

func TestChatOrderManualKeepsNewChatsAtRecentFrontAndCompactsDeletedIDs(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index, id := range []string{"chat-a", "chat-b", "chat-c"} {
		if _, _, err := store.EnsureChatWithSourceAndMode(id, "agent-a", "", id, "", "REACT"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec("UPDATE CHATS SET UPDATED_AT_=? WHERE CHAT_ID_=?", int64(1_780_000_000_000+index), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.MoveChat("chat-a", "chat-c", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureChatWithSourceAndMode("chat-new", "agent-a", "", "new", "", "REACT"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE CHATS SET UPDATED_AT_=? WHERE CHAT_ID_=?", int64(1_790_000_000_000), "chat-new"); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListChatsWithAgentModesAndLimit("", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summaryIDs(items), []string{"chat-new", "chat-a", "chat-c", "chat-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
	if err := store.DeleteChat("chat-c"); err != nil {
		t.Fatal(err)
	}
	state, err := store.SetChatSortMode(SortModeManual)
	if err != nil {
		t.Fatal(err)
	}
	if containsChatID(state.Order, "chat-c") {
		t.Fatalf("deleted chat remained in saved order: %v", state.Order)
	}
}

func TestChatOrderCorruptSidecarFallsBackAndCanBeRebuilt(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.WriteFile(filepath.Join(root, ChatOrderFileName), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.ChatOrder()
	if err != nil {
		t.Fatal(err)
	}
	if state.SortMode != SortModeRecent || state.UpdatedAt != 0 {
		t.Fatalf("fallback state = %+v", state)
	}
	state, err = store.SetChatSortMode(SortModeManual)
	if err != nil {
		t.Fatal(err)
	}
	if state.SortMode != SortModeManual || state.UpdatedAt == 0 {
		t.Fatalf("rebuilt state = %+v", state)
	}
	if _, err := store.readChatOrderLocked(); err != nil {
		t.Fatalf("rebuilt file is invalid: %v", err)
	}
}

func TestChatOrderMoveValidation(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnsureChat("chat-a", "agent-a", "", "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureChat("chat-b", "agent-a", "", "b"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		chatID string
		before string
		after  string
	}{
		{name: "no anchor", chatID: "chat-a"},
		{name: "two anchors", chatID: "chat-a", before: "chat-b", after: "chat-b"},
		{name: "self anchor", chatID: "chat-a", before: "chat-a"},
		{name: "unknown chat", chatID: "missing", before: "chat-a"},
		{name: "unknown anchor", chatID: "chat-a", before: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := store.MoveChat(tt.chatID, tt.before, tt.after); err == nil {
				t.Fatal("MoveChat error = nil")
			}
		})
	}
}

func summaryIDs(items []Summary) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ChatID)
	}
	return ids
}
