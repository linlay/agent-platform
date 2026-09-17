package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionBrowserPolicyAndStaleCancel(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	os.MkdirAll(dir, 0755)
	manifest := connector.Manifest{ID: "demo", Name: "Demo", Version: "1.0.0", Type: "cli", AuthBrowser: "embedded"}
	data, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(dir, "connector.json"), data, 0644)
	os.WriteFile(filepath.Join(dir, "cli.json"), []byte(`{}`), 0644)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	s, err := m.Status(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if s.AuthBrowser != "embedded" {
		t.Fatal(s)
	}
	canceled := false
	done := make(chan struct{})
	close(done)
	m.sessions["demo"] = &login{Session: Session{ID: "new", AuthBrowser: "embedded", Status: "pending"}, cancel: func() { canceled = true }, done: done}
	if m.CancelSession("demo", "old") == nil || canceled {
		t.Fatal("stale cancellation accepted")
	}
	if err := m.CancelSession("demo", "new"); err != nil || !canceled {
		t.Fatal("matching cancellation rejected", err)
	}
}
