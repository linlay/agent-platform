package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAndParseResourceRef(t *testing.T) {
	ref, err := BuildChatScopeRef("artifacts/run_01/夏日 海报 #1%.png")
	if err != nil {
		t.Fatal(err)
	}
	want := "artifacts/run_01/%E5%A4%8F%E6%97%A5%20%E6%B5%B7%E6%8A%A5%20%231%25.png"
	if ref != want {
		t.Fatalf("ref=%q want=%q", ref, want)
	}
	key, err := BuildResourceKey("chat_01", "artifacts/run_01/夏日 海报 #1%.png")
	if err != nil {
		t.Fatal(err)
	}
	chatID, relativePath, err := ParseResourceKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if chatID != "chat_01" || relativePath != "artifacts/run_01/夏日 海报 #1%.png" {
		t.Fatalf("parsed chatID=%q relativePath=%q", chatID, relativePath)
	}
}

func TestParseResourceKeySupportsLegacyDecodedKey(t *testing.T) {
	chatID, relativePath, err := ParseResourceKey("chat_01/旧 图片 #1%.png")
	if err != nil {
		t.Fatal(err)
	}
	if chatID != "chat_01" || relativePath != "旧 图片 #1%.png" {
		t.Fatalf("parsed chatID=%q relativePath=%q", chatID, relativePath)
	}
}

func TestParseResourceKeyRejectsNonLogicalPaths(t *testing.T) {
	for _, value := range []string{
		"/Users/alice/image.png",
		`C:\Users\alice\image.png`,
		"file:///Users/alice/image.png",
		"https://example.com/image.png",
		"chat_01/../secret.png",
		"chat_01/%2e%2e/secret.png",
		"chat_01/.tools/results/call.json",
	} {
		if _, _, err := ParseResourceKey(value); err == nil {
			t.Fatalf("ParseResourceKey(%q) unexpectedly succeeded", value)
		}
	}
}

func TestResolveResourceRejectsSymlinkEscape(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chatID := "chat_symlink"
	if _, _, err := store.EnsureChat(chatID, "agent", "", "hello"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.ChatDir(chatID), "image.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.ResolveResource(chatID + "/image.png"); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
}
