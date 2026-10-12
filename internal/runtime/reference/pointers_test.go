package reference

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
)

func TestChatReferenceContainsOnlyTrustedPointerMetadata(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err = store.EnsureChatWithSource("source", "agent", "secret historical content", "query:alice"); err != nil {
		t.Fatal(err)
	}
	ref, err := New(store).PrepareChatReference(WithSubject(context.Background(), "alice"), "current", queryinput.Reference{Type: "chat", ID: "source", Name: "forged", Meta: map[string]any{"context": "forged body"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := ref.Meta["context"]; body || strings.Contains(ref.Name, "forged") {
		t.Fatalf("untrusted data survived: %#v", ref)
	}
	if _, err = New(store).PrepareChatReference(context.Background(), "current", queryinput.Reference{Type: "chat", ID: "source"}); err == nil {
		t.Fatal("anonymous access to owned Chat")
	}
}
