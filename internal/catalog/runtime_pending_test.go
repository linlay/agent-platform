package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connectortest"
)

func runtimePendingFixture(t *testing.T) (*FileRegistry, config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{
		AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"),
		ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "platform", "connectors"),
		SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams"), StateDir: filepath.Join(root, ".state"),
	}}
	if err := connectortest.WriteCLI(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx"), "dbx", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	body := "key: first\nname: Original\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.dbx\n"
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml"), body)
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "second", "agent.yml"), strings.ReplaceAll(body, "first", "second"))
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r, cfg, body
}

func TestRuntimePendingTracksAgentLoadOutcome(t *testing.T) {
	for _, scenario := range []string{"unchanged", "other_agent", "teams", "skills", "changed", "reverted", "deleted", "invalid_yaml", "invalid_connector"} {
		t.Run(scenario, func(t *testing.T) {
			r, cfg, body := runtimePendingFixture(t)
			before, release, ok := r.AcquireAgentRuntime("first")
			if !ok {
				t.Fatal("lease unavailable")
			}
			t.Cleanup(release)
			called := 0
			r.SetRuntimeReload(func() { called++ })
			path := filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml")
			reason, want := "agents", 0
			switch scenario {
			case "other_agent":
				writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "second", "agent.yml"), strings.ReplaceAll(strings.ReplaceAll(body, "first", "second"), "Original", "Changed"))
			case "teams", "skills":
				reason = scenario
			case "changed", "reverted":
				writeRuntimeAssemblerFile(t, path, strings.ReplaceAll(body, "Original", "Changed"))
				want = 1
			case "deleted":
				if err := os.RemoveAll(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				want = 1
			case "invalid_yaml":
				writeRuntimeAssemblerFile(t, path, "key: [\n")
				want = 1
			case "invalid_connector":
				writeRuntimeAssemblerFile(t, path, strings.ReplaceAll(body, "builtin.dbx", "missing"))
				want = 1
			}
			if err := r.Reload(context.Background(), reason); err != nil {
				t.Fatal(err)
			}
			if scenario == "reverted" {
				writeRuntimeAssemblerFile(t, path, body)
				if err := r.Reload(context.Background(), "agents"); err != nil {
					t.Fatal(err)
				}
				want = 0
			}
			// An unrelated catalog load must neither create nor erase pending work.
			if err := r.Reload(context.Background(), "teams"); err != nil {
				t.Fatal(err)
			}
			current, ok := r.AgentDefinition("first")
			if !ok || current.Name != before.Name {
				t.Fatal("active Agent definition was replaced")
			}
			release()
			if called != want {
				t.Fatalf("release callbacks = %d, want %d", called, want)
			}
			if want == 0 {
				return
			}
			if err := r.Reload(context.Background(), "agents"); err != nil {
				t.Fatal(err)
			}
			current, ok = r.AgentDefinition("first")
			if scenario == "changed" {
				if !ok || current.Name != "Changed" {
					t.Fatal("deferred definition was not published")
				}
			} else if ok {
				t.Fatal("deleted or invalid Agent remained executable after release")
			}
			if scenario == "deleted" {
				if _, err := os.Stat(before.RuntimeDir); !os.IsNotExist(err) {
					t.Fatalf("deleted Agent runtime remains: %v", err)
				}
			}
		})
	}
}

func TestRuntimePendingRetainsOldConnectorVersionAcrossReloads(t *testing.T) {
	r, cfg, _ := runtimePendingFixture(t)
	old, releaseOld, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("lease unavailable")
	}
	t.Cleanup(releaseOld)
	called := 0
	r.SetRuntimeReload(func() { called++ })
	skill := filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx", "skills", "builtin-dbx", "SKILL.md")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeAssemblerFile(t, skill, string(body)+"\nNew version\n")
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	latest, releaseLatest, ok := r.AcquireAgentRuntime("first")
	if !ok || latest.ConnectorMounts[0].Dir == old.ConnectorMounts[0].Dir {
		t.Fatal("connector-only update was not published during the old Run")
	}
	t.Cleanup(releaseLatest)
	// The newest definition is unchanged, but the old Run still owns an old version.
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.ConnectorMounts[0].Dir); err != nil {
		t.Fatalf("active old version removed: %v", err)
	}
	releaseLatest()
	if called != 0 {
		t.Fatal("reconciliation ran before all Agent users left")
	}
	releaseOld()
	if called != 1 {
		t.Fatalf("old connector release callbacks = %d, want 1", called)
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.ConnectorMounts[0].Dir); !os.IsNotExist(err) {
		t.Fatalf("unused old connector version remains: %v", err)
	}
}

func TestRuntimePendingDeferredReloadDoesNotMarkOtherActiveAgent(t *testing.T) {
	r, cfg, body := runtimePendingFixture(t)
	_, releaseFirst, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("first lease unavailable")
	}
	t.Cleanup(releaseFirst)
	_, releaseSecond, ok := r.AcquireAgentRuntime("second")
	if !ok {
		t.Fatal("second lease unavailable")
	}
	t.Cleanup(releaseSecond)
	called := 0
	r.SetRuntimeReload(func() {
		called++
		if err := r.Reload(context.Background(), "agents"); err != nil {
			t.Fatal(err)
		}
	})
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml"), strings.ReplaceAll(body, "Original", "Changed"))
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	releaseFirst()
	if called != 1 {
		t.Fatalf("changed Agent release callbacks = %d, want 1", called)
	}
	releaseSecond()
	if called != 1 {
		t.Fatalf("deferred reload propagated to an unchanged Agent: callbacks = %d", called)
	}
}

func TestRuntimePendingSurvivesFailedReload(t *testing.T) {
	for _, stage := range []string{"scan", "binding"} {
		t.Run(stage, func(t *testing.T) {
			r, cfg, body := runtimePendingFixture(t)
			_, release, ok := r.AcquireAgentRuntime("first")
			if !ok {
				t.Fatal("lease unavailable")
			}
			t.Cleanup(release)
			path := filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml")
			writeRuntimeAssemblerFile(t, path, strings.ReplaceAll(body, "Original", "Changed"))
			if err := r.Reload(context.Background(), "agents"); err != nil {
				t.Fatal(err)
			}
			writeRuntimeAssemblerFile(t, path, body)
			if stage == "scan" {
				// A regular file cannot be scanned as an Agent directory.
				r.cfg.Paths.AgentsDir = path
				if err := r.Reload(context.Background(), "agents"); err == nil {
					t.Fatal("expected Agent root scan to fail")
				}
			} else {
				failure := errors.New("binding failed")
				if err := r.ReloadWithRuntimeBindings(context.Background(), "agents", nil, func() error { return failure }); !errors.Is(err, failure) {
					t.Fatalf("binding error = %v", err)
				}
			}
			called := 0
			r.SetRuntimeReload(func() { called++ })
			release()
			if called != 1 {
				t.Fatalf("failed reload lost pending work: callbacks = %d", called)
			}
		})
	}
}

func TestRuntimePendingPreflightFailurePreservesExistingState(t *testing.T) {
	for _, scenario := range []string{"unchanged", "pending"} {
		t.Run(scenario, func(t *testing.T) {
			r, cfg, body := runtimePendingFixture(t)
			_, release, ok := r.AcquireAgentRuntime("first")
			if !ok {
				t.Fatal("lease unavailable")
			}
			t.Cleanup(release)
			want := 0
			if scenario == "pending" {
				writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml"), strings.ReplaceAll(body, "Original", "Changed"))
				if err := r.Reload(context.Background(), "agents"); err != nil {
					t.Fatal(err)
				}
				want = 1
			}
			failure := errors.New("source preflight failed")
			err := r.ReloadWithRuntimeBindings(context.Background(), "agents", func() error { return failure }, func() error {
				t.Fatal("binding reached after failed preflight")
				return nil
			})
			if !errors.Is(err, failure) {
				t.Fatalf("preflight error = %v", err)
			}
			called := 0
			r.SetRuntimeReload(func() { called++ })
			release()
			if called != want {
				t.Fatalf("preflight failure changed pending state: callbacks = %d, want %d", called, want)
			}
		})
	}
}
