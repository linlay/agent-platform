package rootpaths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceMayContainChatsAndClassifiesChatsFirst(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chats := filepath.Join(workspace, "runtime", "chats")
	chat := filepath.Join(chats, "chat-1")
	for _, dir := range []string{workspace, chats, chat} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := New(workspace, chats, chat)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tests := []struct {
		path string
		want Zone
	}{
		{filepath.Join(chat, "upload.txt"), ZoneCurrentChat},
		{filepath.Join(chats, "chat-2", "upload.txt"), ZoneOtherChat},
		{filepath.Join(workspace, "src", "main.go"), ZoneWorkspace},
		{filepath.Join(root, "external.txt"), ZoneExternal},
	}
	for _, test := range tests {
		got, _, err := roots.Classify(test.path)
		if err != nil {
			t.Fatalf("Classify(%q) error = %v", test.path, err)
		}
		if got != test.want {
			t.Fatalf("Classify(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	masks, err := roots.ContainerMaskedPaths()
	if err != nil {
		t.Fatalf("ContainerMaskedPaths() error = %v", err)
	}
	if len(masks) != 1 || masks[0] != "/workspace/runtime/chats" {
		t.Fatalf("ContainerMaskedPaths() = %#v", masks)
	}
}

func TestWorkspaceRootMayBeFilesystemRoot(t *testing.T) {
	chats := t.TempDir()
	chat := filepath.Join(chats, "chat-1")
	if err := os.MkdirAll(chat, 0o755); err != nil {
		t.Fatal(err)
	}
	roots, err := New(string(filepath.Separator), chats, chat)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if zone, _, err := roots.Classify(filepath.Join(chat, "upload.txt")); err != nil || zone != ZoneCurrentChat {
		t.Fatalf("chat classification = %q, %v", zone, err)
	}
	if _, err := roots.RequireWorkspacePath(filepath.Join(chat, "upload.txt")); !errors.Is(err, ErrPathCrossesChatRoot) {
		t.Fatalf("RequireWorkspacePath() error = %v, want path_crosses_chat_root", err)
	}
}

func TestWorkspaceMustNotEqualOrLiveInsideChatsRoot(t *testing.T) {
	chats := t.TempDir()
	if _, err := New(chats, chats, ""); !errors.Is(err, ErrWorkspaceEqualsChatsRoot) {
		t.Fatalf("equal roots error = %v", err)
	}
	workspace := filepath.Join(chats, "project")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(workspace, chats, ""); !errors.Is(err, ErrWorkspaceInsideChatsRoot) {
		t.Fatalf("nested workspace error = %v", err)
	}
}

func TestCanonicalSymlinkIntoChatsIsNotWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	chats := filepath.Join(workspace, "chats")
	chat := filepath.Join(chats, "chat-1")
	if err := os.MkdirAll(chat, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "chat-link")
	if err := os.Symlink(chat, link); err != nil {
		t.Fatal(err)
	}
	roots, err := New(workspace, chats, chat)
	if err != nil {
		t.Fatal(err)
	}
	if zone, _, err := roots.Classify(filepath.Join(link, "upload.txt")); err != nil || zone != ZoneCurrentChat {
		t.Fatalf("symlink classification = %q, %v", zone, err)
	}
}
