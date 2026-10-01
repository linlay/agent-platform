package chat

import (
	"reflect"
	"testing"
)

func TestNormalizeAgentModesTreatsGeneralAndReactAsOneType(t *testing.T) {
	for _, input := range [][]string{{"GENERAL"}, {"REACT"}, {"REACT", "GENERAL", ""}} {
		if got := NormalizeAgentModes(input); !reflect.DeepEqual(got, []string{"GENERAL", "REACT"}) {
			t.Fatalf("NormalizeAgentModes(%v) = %v", input, got)
		}
	}
	if got := NormalizeAgentModes([]string{"CODER", "CODER"}); !reflect.DeepEqual(got, []string{"CODER"}) {
		t.Fatalf("unrelated modes must not be expanded: %v", got)
	}
}

func TestGeneralFilterMatchesRowsStoredBeforeAndAfterRename(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, mode := range map[string]string{"chat-old": "REACT", "chat-new": "GENERAL", "chat-coder": "CODER"} {
		if _, _, err := s.EnsureChatWithSourceAndMode(id, "agent-a", "", id, "", mode); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListChatsWithOptions(ListOptions{AgentModes: []string{"GENERAL"}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, item := range items {
		seen[item.ChatID] = item.AgentMode
	}
	// The stored spelling is evidence and stays untouched; only the public
	// mapping renames it.
	if len(seen) != 2 || seen["chat-old"] != "REACT" || seen["chat-new"] != "GENERAL" {
		t.Fatalf("GENERAL filter result = %v", seen)
	}
	if PublicAgentMode(seen["chat-old"]) != "GENERAL" || PublicAgentMode("TEAM") != "TEAM" {
		t.Fatalf("unexpected public mode mapping")
	}
}
