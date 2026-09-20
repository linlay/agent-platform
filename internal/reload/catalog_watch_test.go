package reload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/config"
)

type watchTestObserver struct{ reasons chan string }

func (o watchTestObserver) CatalogReloaded(_ context.Context, reason string) { o.reasons <- reason }

type watchTestRegistry struct {
	recordingRuntimeRegistry
	onReload func(string) error
}

func (r *watchTestRegistry) Reload(_ context.Context, reason string) error {
	if r.onReload != nil {
		return r.onReload(reason)
	}
	return nil
}

func newCatalogWatchTest(t *testing.T) (context.Context, *RuntimeCatalogReloader, config.Config, chan string, *watchTestRegistry) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{
		AgentsDir: filepath.Join(root, "agents"), SkillsCenterDir: filepath.Join(root, "skills-center"),
		TeamsDir: filepath.Join(root, "teams"), RegistriesDir: filepath.Join(root, "registries"), ToolsDir: filepath.Join(root, "tools"), RootDir: root,
	}}
	for _, entry := range backgroundWatchEntries(cfg) {
		if err := os.MkdirAll(entry.path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	registry := &watchTestRegistry{}
	r := NewRuntimeCatalogReloader(registry, nil, nil, nil, "", nil)
	reasons := make(chan string, 32)
	r.AddObserver(watchTestObserver{reasons: reasons})
	StartBackgroundReloaders(ctx, cfg, r)
	t.Cleanup(func() {
		cancel()
		for _, group := range r.background.groups {
			select {
			case <-group.watcher.Done():
			case <-time.After(3 * time.Second):
				t.Error("watcher did not stop")
			}
		}
	})
	return ctx, r, cfg, reasons, registry
}

func writeWatchTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogAPIReloadConsumesDuplicateAndLateWatchEvents(t *testing.T) {
	ctx, r, cfg, reasons, _ := newCatalogWatchTest(t)
	path := filepath.Join(cfg.Paths.SkillsCenterDir, "SKILL.md")
	if err := r.WithCatalogDirectoryMutation(ctx, "skills", func(ctx context.Context) error {
		writeWatchTestFile(t, path, "version one")
		return r.Reload(ctx, "skills")
	}); err != nil {
		t.Fatal(err)
	}
	assertReloadReason(t, reasons, "skills", time.Second)
	for range 30 {
		r.background.enqueue("skills")
	}
	assertNoReloadReason(t, reasons, 2*reloadDebounce)
	// An identical write can emit more events; a real equal-size edit must reload.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeWatchTestFile(t, path, "version two")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	assertReloadReason(t, reasons, "skills", 3*time.Second)
	assertNoReloadReason(t, reasons, 2*reloadDebounce)
}

func TestCatalogDirectoryMutationOnlySuspendsItsRoot(t *testing.T) {
	ctx, r, cfg, reasons, _ := newCatalogWatchTest(t)
	err := r.WithCatalogDirectoryMutation(ctx, "skills", func(ctx context.Context) error {
		for _, g := range r.background.groups {
			if g.hasReason("skills") {
				if g.watcher.Watched() != 0 {
					t.Fatal("skill handles retained")
				}
			} else if g.watcher.Watched() == 0 {
				t.Fatalf("unrelated root suspended: %s", g.root)
			}
		}
		writeWatchTestFile(t, filepath.Join(cfg.Paths.SkillsCenterDir, "SKILL.md"), "installed")
		writeWatchTestFile(t, filepath.Join(cfg.Paths.RegistriesDir, "providers", "demo.yml"), "updated")
		return r.Reload(ctx, "skills")
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReloadReason(t, reasons, "skills", time.Second)
	assertReloadReason(t, reasons, "providers", 3*time.Second)
	assertNoReloadReason(t, reasons, 2*reloadDebounce)
}

func TestCatalogSnapshotAndFileMutationKeepWatchHandles(t *testing.T) {
	ctx, r, cfg, reasons, _ := newCatalogWatchTest(t)
	for _, write := range []bool{false, true} {
		err := r.WithCatalogMutation(ctx, func(ctx context.Context) error {
			for _, g := range r.background.groups {
				if g.watcher.Watched() == 0 {
					t.Fatal("ordinary mutation closed watcher")
				}
			}
			if write {
				writeWatchTestFile(t, filepath.Join(cfg.Paths.SkillsCenterDir, "SKILL.md"), "saved")
				return r.Reload(ctx, "skills")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if write {
			assertReloadReason(t, reasons, "skills", time.Second)
		}
		assertNoReloadReason(t, reasons, 2*reloadDebounce)
	}
}

func TestCatalogChangeDuringReloadIsNotAcknowledged(t *testing.T) {
	ctx, r, cfg, reasons, registry := newCatalogWatchTest(t)
	path := filepath.Join(cfg.Paths.SkillsCenterDir, "SKILL.md")
	err := r.WithCatalogDirectoryMutation(ctx, "skills", func(ctx context.Context) error {
		writeWatchTestFile(t, path, "before")
		registry.onReload = func(reason string) error {
			if reason == "skills" {
				writeWatchTestFile(t, path, "during")
				registry.onReload = nil
			}
			return nil
		}
		if err := r.Reload(ctx, "skills"); err != nil {
			return err
		}
		if _, ok := r.background.loaded["skills"]; ok {
			t.Fatal("acknowledged a moving source")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReloadReason(t, reasons, "skills", time.Second)
	assertReloadReason(t, reasons, "skills", 3*time.Second)
	assertNoReloadReason(t, reasons, 2*reloadDebounce)
}

func TestCatalogFailedReloadDoesNotAcknowledgeSource(t *testing.T) {
	ctx, r, cfg, reasons, registry := newCatalogWatchTest(t)
	path := filepath.Join(cfg.Paths.SkillsCenterDir, "SKILL.md")
	err := r.WithCatalogDirectoryMutation(ctx, "skills", func(ctx context.Context) error {
		writeWatchTestFile(t, path, "new")
		registry.onReload = func(string) error { return errors.New("load failed") }
		if r.Reload(ctx, "skills") == nil {
			t.Fatal("missing failure")
		}
		if _, ok := r.background.loaded["skills"]; ok {
			t.Fatal("failure acknowledged")
		}
		registry.onReload = nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReloadReason(t, reasons, "skills", 3*time.Second)
	assertNoReloadReason(t, reasons, 2*reloadDebounce)
}

func TestCatalogWatchGroupsMergeOverlappingRoots(t *testing.T) {
	root := t.TempDir()
	groups := groupWatchEntries([]watchEntry{{filepath.Join(root, "skills"), "skills"}, {root, "agents"}, {root + "-sibling", "teams"}})
	if len(groups) != 2 {
		t.Fatalf("groups=%d", len(groups))
	}
	for _, g := range groups {
		if g.root == root && (!g.hasReason("agents") || !g.hasReason("skills")) {
			t.Fatal("lost overlapping category")
		}
	}
	if resolveChangeReason(root+"-sibling/test", []watchEntry{{root, "agents"}}) != "config" {
		t.Fatal("prefix sibling matched root")
	}
}

func BenchmarkCatalogFingerprint1000Files(b *testing.B) {
	root := b.TempDir()
	content := make([]byte, 4096)
	for i := range 1000 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("skill-%04d.md", i)), content, 0644); err != nil {
			b.Fatal(err)
		}
	}
	entry := watchEntry{path: root, reason: "skills"}
	b.SetBytes(1000 * 4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := catalogFingerprint(context.Background(), entry, nil); err != nil {
			b.Fatal(err)
		}
	}
}
