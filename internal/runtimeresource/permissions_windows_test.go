//go:build windows

package runtimeresource

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrivateTreeACLArgsUseSingleRecursiveCommand(t *testing.T) {
	got := privateTreeACLArgs(`C:\runtime\.agent-platform`, `*S-1-5-21-123`)
	want := []string{
		`C:\runtime\.agent-platform`,
		"/inheritance:r",
		"/grant:r",
		"*S-1-5-21-123:F",
		"*S-1-5-21-123:(OI)(CI)(IO)F",
		"*S-1-5-18:F",
		"*S-1-5-18:(OI)(CI)(IO)F",
		"/T",
		"/C",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("privateTreeACLArgs() = %#v, want %#v", got, want)
	}
}

func TestSecurePrivateTreeKeepsExistingAndFutureFilesReadable(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(nested, "existing.json")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := securePrivateTree(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(existing); err != nil {
		t.Fatalf("read existing protected file: %v", err)
	}

	future := filepath.Join(nested, "future.json")
	if err := os.WriteFile(future, []byte("future"), 0o600); err != nil {
		t.Fatalf("create file under protected directory: %v", err)
	}
	if _, err := os.ReadFile(future); err != nil {
		t.Fatalf("read inherited protected file: %v", err)
	}
}
