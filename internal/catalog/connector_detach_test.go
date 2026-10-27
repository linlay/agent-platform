package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func detachFixture(t *testing.T) (*FileRegistry, config.Config) {
	t.Helper()
	r, cfg, body := runtimePendingFixture(t)
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "demo", "connector.json"), `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":"none"}`)
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "demo", "mcp.json"), `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`)

	for _, key := range []string{"first", "second"} {
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), strings.ReplaceAll(strings.ReplaceAll(body, "first", key), "builtin.dbx", "demo"))
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	return r, cfg
}

func TestConnectorDeletionGateRejectsNewLeasesAndPreservesExisting(t *testing.T) {
	r, _ := detachFixture(t)
	_, release, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("missing runtime")
	}
	gate, users, err := r.BeginConnectorDeletion("demo")
	if err != nil || gate != nil || len(users) != 1 || users[0] != "first" {
		t.Fatalf("active gate: %v %v", users, err)
	}
	// Rejected deletion neither cancels nor releases an existing lease.
	if len(r.liveConnectorMounts) != 1 {
		t.Fatal("existing lease was modified")
	}
	release()
	gate, users, err = r.BeginConnectorDeletion("demo")
	if err != nil || len(users) != 0 || gate == nil {
		t.Fatalf("idle gate: %v %v", users, err)
	}
	defer gate()
	if _, release, ok := r.AcquireAgentRuntime("first"); ok {
		release()
		t.Fatal("new lease admitted during deletion")
	}
	if _, _, release, ok := r.AcquireRunRuntime("first"); ok {
		release()
		t.Fatal("new root Run admitted during deletion")
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	if _, release, ok := r.AcquireAgentRuntime("second"); ok {
		release()
		t.Fatal("reload cleared deletion gate")
	}
	gate()
	if _, release, ok := r.AcquireAgentRuntime("first"); !ok {
		t.Fatal("gate not released")
	} else {
		release()
	}
}

func TestConnectorDetachRollsBackEarlierWritesOnSourceConflict(t *testing.T) {
	r, cfg := detachFixture(t)
	before, err := r.ReadEditableAgentSource("first")
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.PrepareConnectorDetach("demo")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Paths.AgentsDir, "second", "agent.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	external := append(content, []byte("\n# external edit\n")...)
	if err := os.WriteFile(path, external, 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(); !errors.Is(err, ErrAgentSourceConflict) {
		t.Fatalf("conflict: %v", err)
	}
	after, err := r.ReadEditableAgentSource("first")
	if err != nil {
		t.Fatal(err)
	}
	if after.Content != before.Content {
		t.Fatal("first Agent not restored")
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(external) {
		t.Fatal("external edit overwritten")
	}
}

func TestConnectorDetachRecovery(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "package_exists", true: "package_deleted"}[deleted], func(t *testing.T) {
			r, cfg := detachFixture(t)
			before, err := r.ReadEditableAgentSource("first")
			if err != nil {
				t.Fatal(err)
			}
			m, err := r.PrepareConnectorDetach("demo")
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Apply(); err != nil {
				t.Fatal(err)
			}
			if deleted {
				if err := os.RemoveAll(filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "demo")); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverConnectorDetaches(cfg); err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(before.Source.Path)
			if err != nil {
				t.Fatal(err)
			}
			if deleted && strings.Contains(string(current), "- demo") {
				t.Fatal("deleted package was remounted")
			}
			if !deleted && string(current) != before.Content {
				t.Fatal("interrupted detach not restored")
			}
			if _, err := os.Stat(m.path); !os.IsNotExist(err) {
				t.Fatalf("journal retained: %v", err)
			}
		})
	}
}

func TestConnectorDeletionRejectsDurableRunPins(t *testing.T) {
	r, cfg := detachFixture(t)
	mounts := r.PublishedConnectorRuntimes()
	if len(mounts) == 0 {
		t.Fatal("missing mounts")
	}
	if err := cfg.Paths.ConnectorSources().PinRun("suspended", mounts); err != nil {
		t.Fatal(err)
	}
	release, users, err := r.BeginConnectorDeletion("demo")
	if err != nil || release != nil || len(users) != 2 {
		t.Fatalf("durable pins ignored: %v %v", users, err)
	}
}

func TestConnectorDetachRestoresOnlyUnchangedWrittenSources(t *testing.T) {
	r, cfg := detachFixture(t)
	m, err := r.PrepareConnectorDetach("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\n# concurrent edit after detach\n")...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(); !errors.Is(err, ErrAgentSourceConflict) {
		t.Fatalf("rollback: %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(data) {
		t.Fatal("concurrent source overwritten")
	}
	if _, err := os.Stat(m.path); err != nil {
		t.Fatalf("recovery journal missing: %v", err)
	}
}
