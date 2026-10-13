package adminsource

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func connectorImportPlan(t *testing.T, s *ControlService) *ArchiveImportPlan {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range map[string]string{
		"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":null}`,
		"cli.json":       `{}`,
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PrepareArchiveImport(t.Context(), ArchiveImportRequest{ResourceType: "connector", ArchivePath: "/authorized/demo.zip"}, bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plan.Close)
	return plan
}

func TestArchiveImportReceiptLifetimeWaitsForMutationAndKeepsOtherRuns(t *testing.T) {
	s := controlFixture(t)
	first, cancelFirst := context.WithCancel(t.Context())
	second, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	s.importReceipts.Store("first", archiveImportReceipt{RequestDigest: "one", Result: map[string]any{"status": "applied"}})
	s.importReceipts.Store("second", archiveImportReceipt{RequestDigest: "two", Result: map[string]any{"status": "applied"}})
	s.BindArchiveImportLifetime("first", first)
	s.BindArchiveImportLifetime("first", first)
	s.BindArchiveImportLifetime("second", second)
	unlock := s.Mutations.LockSourceMutation()
	cancelFirst()
	if _, _, found := s.ImportReceipt("first", "one"); !found {
		unlock()
		t.Fatal("in-flight mutation receipt was removed early")
	}
	// Simulate the receipt written by a mutation that was already executing when
	// cancellation arrived. Cleanup must wait and then remove that final receipt.
	s.importReceipts.Store("first", archiveImportReceipt{RequestDigest: "one", Result: map[string]any{"status": "committed"}})
	unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, found := s.ImportReceipt("first", "one"); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ended Run retained import receipt")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err, found := s.ImportReceipt("second", "two"); err != nil || !found {
		t.Fatal("cleanup removed another Run's receipt")
	}
}

func TestStreamingTreeRevisionPreservesExistingDigestAndBounds(t *testing.T) {
	root := t.TempDir()
	files := []struct {
		name string
		data []byte
	}{
		{"a.bin", bytes.Repeat([]byte("archive data"), 1<<16)},
		{"nested/b.md", []byte("text")},
	}
	legacy := sha256.New()
	var size int64
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.data, 0640); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(legacy, "%s\x00%d\x00%d\x00", file.name, info.Mode().Perm(), len(file.data))
		legacy.Write(file.data)
		size += info.Size()
	}
	got, exists, err := treeRevisionWithLimit(root, size)
	if err != nil || !exists || got != hex.EncodeToString(legacy.Sum(nil)) {
		t.Fatalf("streaming revision changed contract: %s %v", got, err)
	}
	if _, _, err := treeRevisionWithLimit(root, size-1); err == nil {
		t.Fatal("tree size bound was bypassed")
	}
}

func TestArchiveImportRejectsChangedStagingBeforeConsumingApproval(t *testing.T) {
	s := controlFixture(t)
	p := connectorImportPlan(t, s)
	if err := os.WriteFile(filepath.Join(p.Connector.Dir, "cli.json"), []byte(`{"changed":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	consumed := false
	_, err := s.ApplyArchiveImport(t.Context(), p, "invocation", "request", func(string) bool { consumed = true; return true })
	if err == nil || !strings.Contains(err.Error(), "approval_stale") || consumed {
		t.Fatalf("changed candidate: %v consumed=%v", err, consumed)
	}
	if _, err := os.Stat(filepath.Join(s.Config.Paths.EffectiveConnectorsCenterDir(), "demo")); !os.IsNotExist(err) {
		t.Fatal("changed candidate was published")
	}
}

func TestArchiveImportRechecksDestinationInsideCatalogCoordination(t *testing.T) {
	s := controlFixture(t)
	p := connectorImportPlan(t, s)
	path := filepath.Join(s.Config.Paths.EffectiveConnectorsCenterDir(), "demo", "connector.json")
	s.Coordinate = func(ctx context.Context, reason string, mutate func(context.Context) error) error {
		if reason != "connectors" {
			t.Fatal(reason)
		}
		putControlFile(t, path, `{"id":"demo","name":"Concurrent source","version":"2.0.0","type":"cli","auth_mode":null}`)
		return mutate(ctx)
	}
	consumed := false
	_, err := s.ApplyArchiveImport(t.Context(), p, "invocation", "request", func(string) bool { consumed = true; return true })
	if err == nil || !strings.Contains(err.Error(), "revision_conflict") || consumed {
		t.Fatalf("concurrent destination: %v consumed=%v", err, consumed)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("Concurrent source")) {
		t.Fatal("concurrent source was overwritten")
	}
}
