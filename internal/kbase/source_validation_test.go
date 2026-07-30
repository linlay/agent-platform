package kbase

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateWorkspaceChatsSeparation(t *testing.T) {
	root := t.TempDir()
	chatsRoot := filepath.Join(root, "runtime", "chats")
	separateRoot := filepath.Join(root, "knowledge")
	for _, path := range []string{chatsRoot, separateRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name          string
		workspaceRoot string
		wantErr       bool
	}{
		{name: "separate", workspaceRoot: separateRoot},
		{name: "filesystem_root", workspaceRoot: string(filepath.Separator), wantErr: true},
		{name: "same", workspaceRoot: chatsRoot, wantErr: true},
		{name: "workspace_contains_chats", workspaceRoot: filepath.Dir(filepath.Dir(chatsRoot)), wantErr: true},
		{name: "workspace_under_chats", workspaceRoot: filepath.Join(chatsRoot, "knowledge"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.MkdirAll(test.workspaceRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			err := ValidateWorkspaceChatsSeparation(test.workspaceRoot, chatsRoot)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestValidateWorkspaceChatsSeparationUsesCanonicalSymlinkTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup is not portable on Windows")
	}
	root := t.TempDir()
	chatsRoot := filepath.Join(root, "runtime", "chats")
	if err := os.MkdirAll(chatsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceLink := filepath.Join(root, "knowledge-link")
	if err := os.Symlink(chatsRoot, sourceLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateWorkspaceChatsSeparation(sourceLink, chatsRoot); err == nil {
		t.Fatal("symlinked source resolving to chats root must be rejected")
	}
}
