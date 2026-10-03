package conversation

import (
	"agent-platform/internal/chat"
	"testing"
)

func TestControlArchiveBatchContinuesAndRestores(t *testing.T) {
	s, store, c := controlChatFixture(t)
	controlAppendRun(t, store, "target", "run-1", "finished")
	archive, err := chat.NewArchiveStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Archives = archive
	s.Archiver = chat.NewArchiver(store, archive)
	// Failure before and after a valid target must not stop or undo it.
	ids := []any{"current", "target", "foreign", "missing"}
	result, err := s.ControlManage(c, "archive", map[string]any{"chatIds": ids}, "")
	if err != nil || result["succeeded"] != 1 || result["failed"] != 3 {
		t.Fatalf("%v %v", result, err)
	}
	if _, err = s.ResolveControlChat(c, "target", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveControlChat(c, "foreign", false); err == nil {
		t.Fatal("foreign owner admitted")
	}
	result, err = s.ControlManage(c, "restore", map[string]any{"chatIds": []any{"missing", "target"}}, "")
	if err != nil || result["succeeded"] != 1 || result["failed"] != 1 {
		t.Fatalf("%v %v", result, err)
	}
	if _, err = s.ResolveControlChat(c, "target", false); err != nil {
		t.Fatal(err)
	}
}

func TestControlArchiveBatchValidatesBeforeMutating(t *testing.T) {
	s, _, c := controlChatFixture(t)
	for _, args := range []map[string]any{
		{}, {"chatIds": []any{}}, {"chatIds": []any{"target", "target"}},
		{"chatIds": []any{"target", 3}}, {"chatIds": []any{"target", "../bad"}},
		{"chatId": "target", "chatIds": []any{"target"}},
	} {
		if _, err := s.ControlManage(c, "archive", args, ""); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := s.ResolveControlChat(c, "target", false); err != nil {
		t.Fatal(err)
	}
}
