package chat

import (
	"strings"
	"testing"
)

func TestInitialChatNameStoredWithoutMessageTruncation(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("名字", 30)
	summary, created, err := store.EnsureChatWithInitialName("named", "agent", "message", "run-query:caller", "GENERAL", name)
	if err != nil || !created || summary.ChatName != name {
		t.Fatalf("%#v %v", summary, err)
	}
	if _, _, err := store.EnsureChatWithInitialName("named", "agent", "next", "run-query:caller", "GENERAL", "replacement"); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.PromotePendingChatName("named", "next"); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Summary("named")
	if err != nil || persisted.ChatName != name {
		t.Fatalf("%#v %v", persisted, err)
	}
	defaultSummary, _, err := reopened.EnsureChatWithInitialName("unnamed", "agent", "message", "", "GENERAL", "")
	if err != nil || defaultSummary.ChatName != "message" {
		t.Fatalf("%#v %v", defaultSummary, err)
	}
}
