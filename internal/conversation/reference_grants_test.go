package conversation

import "testing"

func TestAnonymousReferenceGrantOnlyAllowsExplicitRead(t *testing.T) {
	s, store, caller := controlChatFixture(t)
	caller.Subject = ""
	if _, _, err := store.EnsureChatWithSourceAndMode("unowned", "agent", "Unowned", "", "GENERAL"); err != nil {
		t.Fatal(err)
	}
	caller.ReferencedChats = []string{"unowned", "foreign"}
	if _, err := s.ControlRead(caller, map[string]any{"chatId": "unowned", "view": "summary"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveControlChat(caller, "unowned", false); err == nil {
		t.Fatal("read grant expanded management access")
	}
	if _, err := s.ControlRead(caller, map[string]any{"chatId": "foreign", "view": "summary"}); err == nil {
		t.Fatal("read grant bypassed ownership")
	}
	caller.ReferencedChats = nil
	if _, err := s.ControlRead(caller, map[string]any{"chatId": "unowned", "view": "summary"}); err == nil {
		t.Fatal("unreferenced Chat accepted")
	}
}
