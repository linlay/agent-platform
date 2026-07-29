package kbase

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateSourceChatsSeparation(t *testing.T) {
	root := t.TempDir()
	chatsRoot := filepath.Join(root, "runtime", "chats")
	separateRoot := filepath.Join(root, "knowledge")
	for _, path := range []string{chatsRoot, separateRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		sourceRoot string
		wantErr    bool
	}{
		{name: "separate", sourceRoot: separateRoot},
		{name: "same", sourceRoot: chatsRoot, wantErr: true},
		{name: "source_contains_chats", sourceRoot: filepath.Dir(filepath.Dir(chatsRoot)), wantErr: true},
		{name: "source_under_chats", sourceRoot: filepath.Join(chatsRoot, "knowledge"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.MkdirAll(test.sourceRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			err := ValidateSourceChatsSeparation(test.sourceRoot, chatsRoot)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestValidateSourceChatsSeparationUsesCanonicalSymlinkTargets(t *testing.T) {
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
	if err := ValidateSourceChatsSeparation(sourceLink, chatsRoot); err == nil {
		t.Fatal("symlinked source resolving to chats root must be rejected")
	}
}
