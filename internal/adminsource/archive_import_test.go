package adminsource

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
