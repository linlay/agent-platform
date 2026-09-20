package agentconfig

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityReadAllowsAtomicRefreshAndRevocationWindows(t *testing.T) {
	file := filepath.Join(t.TempDir(), "identity-token.txt")
	if err := os.WriteFile(file, []byte("fixture-old"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := openIdentityFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	staging := file + ".tmp"
	if err := os.WriteFile(staging, []byte("fixture-new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, file); err != nil {
		t.Fatalf("refresh blocked by reader: %v", err)
	}
	old, err := io.ReadAll(reader)
	if err != nil || string(old) != "fixture-old" {
		t.Fatal("existing read lost its original snapshot")
	}
	if got, err := ReadAccessTokenFile(file); err != nil || got != "fixture-new" {
		t.Fatal("fresh invocation did not read refreshed token")
	}
	current, err := openIdentityFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := os.Remove(file); err != nil {
		t.Fatalf("revocation blocked by reader: %v", err)
	}
	remaining, err := io.ReadAll(current)
	if err != nil || string(remaining) != "fixture-new" {
		t.Fatal("revocation interrupted the in-flight identity read")
	}
	// Windows retains a delete-pending file while a handle is open. Close the
	// in-flight read before asserting the next invocation sees no identity.
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadAccessTokenFile(file); err != nil || got != "" {
		t.Fatal("revoked token remained available")
	}
}
