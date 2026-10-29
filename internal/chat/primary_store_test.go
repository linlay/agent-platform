package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func newPrimaryTestStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("chat-%02d", i)
		agentKey := "agent-a"
		if i >= 4 {
			agentKey = "agent-b"
		}
		if _, _, err := s.EnsureChatWithSourceAndMode(id, agentKey, id, "", "GENERAL"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE CHATS SET UPDATED_AT_=? WHERE CHAT_ID_=?", int64(1_780_000_000_000+i), id); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestChatPrimaryPointerReplacesPreviousAndMarksSummaries(t *testing.T) {
	s := newPrimaryTestStore(t)
	if id, err := s.PrimaryChatID("agent-a"); err != nil || id != "" {
		t.Fatalf("missing file should mean no primary: %q %v", id, err)
	}
	change, err := s.SetChatPrimary("agent-a", "chat-01")
	if err != nil || !change.Changed || change.PreviousChatID != "" {
		t.Fatalf("first set: %+v %v", change, err)
	}
	change, err = s.SetChatPrimary("agent-a", "chat-03")
	if err != nil || !change.Changed || change.PreviousChatID != "chat-01" {
		t.Fatalf("replace: %+v %v", change, err)
	}
	change, err = s.SetChatPrimary("agent-a", "chat-03")
	if err != nil || change.Changed || change.PreviousChatID != "chat-03" {
		t.Fatalf("same chat must be a no-op: %+v %v", change, err)
	}
	if _, err := s.SetChatPrimary("agent-b", "chat-05"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(s.root, ChatPrimaryFileName))
	if err != nil {
		t.Fatal(err)
	}
	var state PrimaryState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || state.UpdatedAt <= 0 || len(state.Agents) != 2 ||
		state.Agents["agent-a"].ChatID != "chat-03" || state.Agents["agent-b"].ChatID != "chat-05" || state.Agents["agent-a"].Since <= 0 {
		t.Fatalf("unexpected file: %s", data)
	}

	summary, err := s.Summary("chat-03")
	if err != nil || !summary.Primary {
		t.Fatalf("summary not primary: %+v %v", summary, err)
	}
	if summary, _ := s.Summary("chat-01"); summary.Primary {
		t.Fatal("previous primary still marked")
	}

	yes, no := true, false
	items, err := s.ListChatsWithOptions(ListOptions{Primary: &yes})
	if err != nil || !reflect.DeepEqual(summaryIDs(items), []string{"chat-05", "chat-03"}) {
		t.Fatalf("primary list: %v %v", summaryIDs(items), err)
	}
	items, err = s.ListChatsWithOptions(ListOptions{AgentKey: "agent-a", Primary: &no, Limit: 2})
	if err != nil || !reflect.DeepEqual(summaryIDs(items), []string{"chat-02", "chat-01"}) {
		t.Fatalf("history list must filter before limit: %v %v", summaryIDs(items), err)
	}
	items, err = s.ListChatsWithOptions(ListOptions{AgentKey: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Primary != (item.ChatID == "chat-03") {
			t.Fatalf("list flag wrong for %s: %v", item.ChatID, item.Primary)
		}
	}
}

func TestChatPrimaryRejectsForeignOrMissingChat(t *testing.T) {
	s := newPrimaryTestStore(t)
	var validationErr *OrderValidationError
	if _, err := s.SetChatPrimary("agent-a", "chat-05"); !errors.As(err, &validationErr) {
		t.Fatalf("chat of another agent accepted: %v", err)
	}
	if _, err := s.SetChatPrimary("agent-a", "chat-99"); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("missing chat: %v", err)
	}
	if _, err := s.SetChatPrimary("", "chat-01"); !errors.As(err, &validationErr) {
		t.Fatalf("empty agent key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, ChatPrimaryFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected mutations must not create the file")
	}
}

func TestChatPrimaryClearedOnDelete(t *testing.T) {
	s := newPrimaryTestStore(t)
	if _, err := s.SetChatPrimary("agent-a", "chat-02"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChat("chat-02"); err != nil {
		t.Fatal(err)
	}
	state, err := s.readChatPrimaryLocked()
	if err != nil || len(state.Agents) != 0 {
		t.Fatalf("pointer survived delete: %+v %v", state, err)
	}
	if id, err := s.PrimaryChatID("agent-a"); err != nil || id != "" {
		t.Fatalf("deleted chat still primary: %q %v", id, err)
	}
}

func TestChatPrimaryDamagedFileDegradesReadsAndBlocksMutations(t *testing.T) {
	s := newPrimaryTestStore(t)
	path := filepath.Join(s.root, ChatPrimaryFileName)
	damaged := []byte(`{"version":1,"agents":{"agent-a":{"chatId":"chat-01","since":1}},"updatedAt":1,"extra":true}`)
	if err := os.WriteFile(path, damaged, 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListChatsWithOptions(ListOptions{AgentKey: "agent-a"})
	if err != nil || len(items) != 4 {
		t.Fatalf("list must survive damaged preference: %v %v", summaryIDs(items), err)
	}
	for _, item := range items {
		if item.Primary {
			t.Fatal("damaged preference marked a primary chat")
		}
	}
	if _, err := s.SetChatPrimary("agent-a", "chat-02"); err == nil {
		t.Fatal("mutation accepted a damaged file")
	}
	if _, err := s.PrimaryChatID("agent-a"); err == nil {
		t.Fatal("strict read accepted a damaged file")
	}
	if after, _ := os.ReadFile(path); string(after) != string(damaged) {
		t.Fatal("damaged file was overwritten")
	}
}

func TestChatPrimaryRejectsDuplicateChatAcrossAgents(t *testing.T) {
	s := newPrimaryTestStore(t)
	data := []byte(`{"version":1,"agents":{"agent-a":{"chatId":"chat-01","since":1},"agent-b":{"chatId":"chat-01","since":1}},"updatedAt":1}`)
	if err := os.WriteFile(filepath.Join(s.root, ChatPrimaryFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readChatPrimaryLocked(); err == nil {
		t.Fatal("duplicate chat id accepted")
	}
}

func TestChatPrimaryBackfillNeverReplacesActivePointer(t *testing.T) {
	s := newPrimaryTestStore(t)
	change, err := s.SetChatPrimaryIfMissing("agent-a", "chat-01")
	if err != nil || !change.Changed {
		t.Fatalf("missing pointer must be filled: %+v %v", change, err)
	}
	change, err = s.SetChatPrimaryIfMissing("agent-a", "chat-02")
	if err != nil || change.Changed || change.PreviousChatID != "chat-01" {
		t.Fatalf("backfill replaced an active primary: %+v %v", change, err)
	}
	if id, _ := s.PrimaryChatID("agent-a"); id != "chat-01" {
		t.Fatalf("primary = %q", id)
	}
	if err := s.DeleteChat("chat-01"); err != nil {
		t.Fatal(err)
	}
	change, err = s.SetChatPrimaryIfMissing("agent-a", "chat-02")
	if err != nil || !change.Changed {
		t.Fatalf("a pointer to a deleted Chat counts as missing: %+v %v", change, err)
	}
}
