package chatresource

import (
	"agent-platform/internal/chat"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactsRequireChatResolveAmbiguityAndVerifyContent(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"chat-a", "chat-b"} {
		if _, _, err = store.EnsureChat(id, "agent", "", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("published")
	hash := sha256.Sum256(data)
	relative := "artifacts/run-a/result.txt"
	path := filepath.Join(store.ChatDir("chat-a"), filepath.FromSlash(relative))
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, data, 0600)
	item := map[string]any{"artifactId": "same", "url": relative, "name": "result.txt", "sizeBytes": len(data), "sha256": hex.EncodeToString(hash[:]), "mimeType": "text/plain"}
	if err = store.AppendArtifactManifest("chat-a", "run-a", 1, []map[string]any{item}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if _, err = service.GetArtifact("chat-b", "same", ""); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatal("cross-chat resolution", err)
	}
	f, meta, err := service.OpenArtifact("chat-a", "same", "run-a")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if meta.RunID != "run-a" || meta.ChatID != "chat-a" {
		t.Fatal(meta)
	}
	os.WriteFile(path, []byte("modified!"), 0600)
	if _, _, err = service.OpenArtifact("chat-a", "same", ""); !errors.Is(err, ErrArtifactChanged) {
		t.Fatal("changed content accepted", err)
	}
	if err = store.AppendArtifactManifest("chat-a", "run-b", 2, []map[string]any{item}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetArtifact("chat-a", "same", ""); !errors.Is(err, ErrArtifactAmbiguous) {
		t.Fatal("ambiguous ID picked first", err)
	}
	if _, err = service.GetArtifact("chat-a", "same", "run-a"); err != nil {
		t.Fatal(err)
	}
	items, more, err := service.ListArtifacts("chat-a", "", 0, 1)
	if err != nil || !more || len(items) != 1 {
		t.Fatal(items, more, err)
	}
	items, more, err = service.ListArtifacts("chat-a", "run-b", 0, 10)
	if err != nil || more || len(items) != 1 || items[0].RunID != "run-b" {
		t.Fatal(items, more, err)
	}
}
