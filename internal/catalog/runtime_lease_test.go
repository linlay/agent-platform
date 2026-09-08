package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestAgentRuntimeLeaseDefersOnlyActiveAgentsAndKeepsCredentialState(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "platform", "connectors"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams"), StateDir: filepath.Join(root, ".state")}}
	if err := connector.WriteBuiltin(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.dbx"), "dbx", "1.0.0", "darwin"); err != nil {
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
	firstSkill := filepath.Join(first.RuntimeDir, "connectors", "builtin.dbx", "skills", "builtin-dbx", "SKILL.md")
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
	assertRuntimeAssemblerContent(t, filepath.Join(second.RuntimeDir, "connectors", "builtin.dbx", "skills", "builtin-dbx", "SKILL.md"), strings.TrimSpace(string(before)+"\nUpdated\n"))
	called := 0
	r.SetRuntimeReload(func() { called++ })
	release()
	release()
	if called != 1 {
		t.Fatalf("deferred reload callbacks: %d", called)
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
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
