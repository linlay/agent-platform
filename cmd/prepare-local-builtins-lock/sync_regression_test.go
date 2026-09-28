package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendoredComponentDoesNotInheritParentGit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("parent"), 0644); err != nil {
		t.Fatal(err)
	}
	initGitRepository(t, root)
	child := filepath.Join(root, "vendor/component")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	commit, hasCommit, err := localComponentCommit(child)
	if err != nil || hasCommit || commit != "" {
		t.Fatalf("inherited parent provenance: %q %v %v", commit, hasCommit, err)
	}
}

func TestAutomaticSyncStillChecksRebuiltArchive(t *testing.T) {
	_, lock, collection, durable, _ := promotionFixture(t, false)
	var output bytes.Buffer
	if err := offerUpdate(lock, collection, durable, "darwin/arm64", strings.NewReader("yes\n"), &output, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := prepareRolloutCandidate(lock, collection, durable, "darwin/arm64")
	if err != nil || len(candidate.Updates) != 0 || len(candidate.Notices) != 0 {
		t.Fatalf("identical rebuild: %+v %v", candidate, err)
	}
	archive := filepath.Join(collection, "dbx/dist/v1.2.0/dbx_v1.2.0_darwin_arm64.tar.gz")
	// Change a valid gzip header: genuine archive differences must still be checked.
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes[9] ^= 1 // OS metadata byte; leaves the compressed payload valid.
	if err := os.WriteFile(archive, archiveBytes, 0644); err != nil {
		t.Fatal(err)
	}
	candidate, err = prepareRolloutCandidate(lock, collection, durable, "darwin/arm64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(candidate.Notices, "\n"), "immutable version conflict") {
		t.Fatalf("different rebuilt archive was not checked: %+v", candidate)
	}
	after, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("same-version check rewrote lock")
	}
}
