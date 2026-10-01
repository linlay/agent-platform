package catalog

import (
	"agent-platform/internal/connectortest"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestAgentRuntimeLeaseReleaseWithoutReloadKeepsConnectorMount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		users int
	}{{"single", 1}, {"concurrent", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "platform", "connectors"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams"), StateDir: filepath.Join(root, ".state")}}
			if err := connectortest.WriteCLI(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx"), "dbx", "1.0.0"); err != nil {
				t.Fatal(err)
			}
			writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "demo", "agent.yml"), "key: demo\nname: Test\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.dbx\n")
			r, err := NewFileRegistry(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			var called atomic.Int32
			r.SetRuntimeReload(func() { called.Add(1) })
			var releases []func()
			var mount ConnectorMount
			for i := 0; i < tc.users; i++ {
				def, release, ok := r.AcquireAgentRuntime("demo")
				if !ok || len(def.ConnectorMounts) != 1 {
					t.Fatal("expected Agent runtime with one connector")
				}
				t.Cleanup(release)
				releases = append(releases, release)
				mount = def.ConnectorMounts[0]
			}
			var wg sync.WaitGroup
			for _, release := range releases {
				wg.Add(1)
				go func() {
					defer wg.Done()
					release()
					release() // Repeated release must remain idempotent.
				}()
			}
			wg.Wait()
			if got := called.Load(); got != 0 {
				t.Fatalf("release without changes triggered %d reload callbacks", got)
			}
			if len(r.runtimeUsers) != 0 || len(r.liveConnectorUsers) != 0 || len(r.liveConnectorMounts) != 0 {
				t.Fatal("released runtime still has active references")
			}
			def, ok := r.AgentDefinition("demo")
			if !ok || len(def.ConnectorMounts) != 1 || def.ConnectorMounts[0] != mount {
				t.Fatal("release changed the published connector mount")
			}
			runtimes := r.ConnectorRuntimes()
			if len(runtimes) != 1 || runtimes[0].Dir != mount.Dir {
				t.Fatalf("published connector missing from runtime references: %#v", runtimes)
			}
			if err := r.assembler.connectors.CollectShared(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(mount.Dir); err != nil {
				t.Fatalf("published connector was not protected after release: %v", err)
			}
		})
	}
}

func TestAgentRuntimeLeaseDefersOnlyActiveAgentsAndKeepsCredentialState(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "platform", "connectors"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams"), StateDir: filepath.Join(root, ".state")}}
	if err := connectortest.WriteCLI(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx"), "dbx", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first", "second"} {
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), "key: "+key+"\nname: Test\nmode: REACT\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.dbx\n")
	}
	sourceSkill := filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx", "skills", "builtin-dbx")
	linked := os.Symlink(filepath.Join("references", "commands.md"), filepath.Join(sourceSkill, "commands-link.md")) == nil
	state := filepath.Join(cfg.Paths.StateDir, "connectors", "external", "oauth.json")
	writeRuntimeAssemblerFile(t, state, "private credential")
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, release, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("lease unavailable")
	}
	defer release()
	_, releaseSecond, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("second lease unavailable")
	}
	defer releaseSecond()
	firstSkill := filepath.Join(first.ConnectorMounts[0].Dir, "skills", "builtin-dbx", "SKILL.md")
	if linked {
		info, err := os.Lstat(filepath.Join(filepath.Dir(firstSkill), "commands-link.md"))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("Agent publication lost package symlink")
		}
	}
	before, err := os.ReadFile(firstSkill)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx", "skills", "builtin-dbx", "SKILL.md")
	writeRuntimeAssemblerFile(t, source, strings.TrimSpace(string(before)+"\nUpdated\n"))
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	assertRuntimeAssemblerContent(t, firstSkill, strings.TrimSpace(string(before)))
	second, _ := r.AgentDefinition("second")
	assertRuntimeAssemblerContent(t, filepath.Join(second.ConnectorMounts[0].Dir, "skills", "builtin-dbx", "SKILL.md"), strings.TrimSpace(string(before)+"\nUpdated\n"))
	called := 0
	r.SetRuntimeReload(func() { called++ })
	release()
	release()
	if called != 0 {
		t.Fatal("deferred reload ran while another user was still active")
	}
	releaseSecond()
	releaseSecond()
	if called != 1 {
		t.Fatalf("deferred reload callbacks: %d", called)
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	latest, _ := r.AgentDefinition("first")
	if latest.ConnectorMounts[0].Dir == first.ConnectorMounts[0].Dir {
		t.Fatal("new Run did not receive new version")
	}
	if _, err := os.Stat(first.ConnectorMounts[0].Dir); !os.IsNotExist(err) {
		t.Fatal("released old version was not collected")
	}
	firstSkill = filepath.Join(latest.ConnectorMounts[0].Dir, "skills", "builtin-dbx", "SKILL.md")
	assertRuntimeAssemblerContent(t, firstSkill, strings.TrimSpace(string(before)+"\nUpdated\n"))
	_, holdDeleted, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("cannot retain Agent before deletion")
	}
	defer holdDeleted()
	if err := os.RemoveAll(filepath.Join(cfg.Paths.AgentsDir, "first")); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(firstSkill); err != nil {
		t.Fatal("active Agent files removed after source deletion")
	}
	holdDeleted()
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("deleted Agent runtime remains")
	}
	assertRuntimeAssemblerContent(t, state, "private credential")
}

func TestRuntimePublicationValidatesBeforeFilesAndBindsBeforeNewLease(t *testing.T) {
	r := &FileRegistry{agents: map[string]AgentDefinition{"demo": {Key: "demo"}}}
	invalid := errors.New("invalid connector source")
	bound := false
	if err := r.ReloadWithRuntimeBindings(context.Background(), "teams", func() error { return invalid }, func() error { bound = true; return nil }); !errors.Is(err, invalid) || bound {
		t.Fatal("invalid source reached publication")
	}
	// teams is sufficient to exercise the same admission/publication mutex
	// without needing another filesystem fixture.
	r.cfg.Paths.TeamsDir = t.TempDir()
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- r.ReloadWithRuntimeBindings(context.Background(), "teams", nil, func() error { close(entered); <-finish; return nil })
	}()
	<-entered
	leased := make(chan func(), 1)
	go func() { _, release, _ := r.AcquireAgentRuntime("demo"); leased <- release }()
	select {
	case release := <-leased:
		close(finish)
		release()
		t.Fatal("new lease admitted before component routes were bound")
	case <-time.After(30 * time.Millisecond):
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case release := <-leased:
		release()
	case <-time.After(time.Second):
		t.Fatal("lease remained blocked after publication")
	}
}
