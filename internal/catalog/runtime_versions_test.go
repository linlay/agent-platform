package catalog

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/runtimeskills"
)

func TestSkillSnapshotNormalizesExecutableBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable permission normalization")
	}
	root := t.TempDir()
	a, err := newVersionTestAssembler(t, filepath.Join(root, "ru-agents"), filepath.Join(root, "center"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	writeRuntimeAssemblerSkill(t, source, "Skill")
	script := filepath.Join(source, "script.sh")
	writeRuntimeAssemblerFile(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0610); err != nil {
		t.Fatal(err)
	}
	digest, err := a.installSkill(source)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := runtimeskills.Path(runtimeskills.Root(a.root), digest)
	info, err := os.Stat(filepath.Join(dir, "script.sh"))
	if err != nil || info.Mode().Perm() != 0500 {
		t.Fatal("executable was not normalized and sealed", err)
	}
}

func TestRuntimeVersionsShareSkillsAndReleaseIndependently(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	for _, key := range []string{"one", "two"} {
		writeRuntimeAssemblerAgent(t, cfg.Paths.AgentsDir, key, []string{"shared"})
	}
	writeRuntimeAssemblerSkill(t, filepath.Join(cfg.Paths.SkillsCenterDir, "shared"), "Shared")
	r, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, release, ok := r.AcquireAgentRuntime("one")
	if !ok {
		t.Fatal("missing lease")
	}
	defer release()
	other, _ := r.AgentDefinition("two")
	oneSkill, _ := runtimeskills.Resolve(old.RuntimeDir, "shared")
	twoSkill, _ := runtimeskills.Resolve(other.RuntimeDir, "shared")
	if oneSkill != twoSkill {
		t.Fatal("identical Skills were duplicated")
	}
	if entries, err := os.ReadDir(filepath.Join(old.RuntimeDir, "skills", "shared")); err != nil || len(entries) != 0 {
		t.Fatal("Skill mountpoint must be empty", err)
	}
	source := filepath.Join(cfg.Paths.AgentsDir, "one", "agent.yml")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeAssemblerFile(t, source, strings.Replace(string(body), "name: one", "name: Updated", 1))
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	latest, releaseLatest, ok := r.AcquireAgentRuntime("one")
	if !ok {
		t.Fatal("new version unavailable")
	}
	defer releaseLatest()
	if latest.Name != "Updated" || latest.RuntimeDir == old.RuntimeDir || old.Name != "one" {
		t.Fatal("definition or version was not frozen")
	}
	latestSkill, _ := runtimeskills.Resolve(latest.RuntimeDir, "shared")
	if latestSkill != oneSkill {
		t.Fatal("config-only update copied Skill")
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	same, _ := r.AgentDefinition("one")
	if same.RuntimeDir != latest.RuntimeDir {
		t.Fatal("unchanged reload created a version")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, done, ok := r.AcquireAgentRuntime("one")
			if !ok {
				t.Error("lease failed")
				return
			}
			if d.RuntimeDir != latest.RuntimeDir {
				t.Error("wrong version")
			}
			done()
			done()
		}()
	}
	wg.Wait()
	release()
	if _, err := os.Stat(old.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("old version not collected", err)
	}
	if _, err := os.Stat(oneSkill); err != nil {
		t.Fatal("shared Skill collected while referenced", err)
	}
	latest.Skills[0] = "changed"
	actual, _ := r.AgentDefinition("one")
	if actual.Skills[0] != "shared" {
		t.Fatal("definition slice leaked")
	}
}

func TestRuntimeReleaseCollectsOnlyWhenVersionBecomesIdle(t *testing.T) {
	r, cfg, _ := runtimePendingFixture(t)
	def, first, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("missing first lease")
	}
	defer first()
	_, second, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("missing second lease")
	}
	defer second()
	// An unreferenced cache directory makes a GC pass observable without
	// adding test hooks to the production filesystem path.
	orphan := filepath.Join(cfg.Paths.EffectiveRUSkillsDir(), strings.Repeat("a", 64))
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	first()
	first() // Duplicate release must neither decrement again nor trigger GC.
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("non-final release scanned the shared Skill cache", err)
	}
	second()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("last release did not collect unreferenced Skill", err)
	}
	if _, err := os.Stat(def.RuntimeDir); err != nil {
		t.Fatal("published version was collected", err)
	}
}

func TestCorruptSkillBlocksNewLeasesUntilUnusedAndRebuilt(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	writeRuntimeAssemblerAgent(t, cfg.Paths.AgentsDir, "one", []string{"shared"})
	writeRuntimeAssemblerSkill(t, filepath.Join(cfg.Paths.SkillsCenterDir, "shared"), "Shared")
	r, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, done, ok := r.AcquireAgentRuntime("one")
	if !ok {
		t.Fatal("missing lease")
	}
	defer done()
	dir, _ := runtimeskills.Resolve(d.RuntimeDir, "shared")
	info, _ := os.Stat(dir)
	if info.Mode().Perm()&0222 != 0 {
		t.Fatal("Skill directory writable")
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, release, ok := r.AcquireAgentRuntime("one"); ok {
		release()
		t.Fatal("corrupt shared Skill admitted")
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "corrupt" {
		t.Fatal("leased corrupt path was overwritten")
	}
	done()
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	rebuilt, release, ok := r.AcquireAgentRuntime("one")
	if !ok {
		t.Fatal("unused corrupt snapshot not rebuilt")
	}
	release()
	if rebuilt.RuntimeRevision != d.RuntimeRevision {
		t.Fatal("same source changed identity")
	}
}

func TestRuntimeVersionStartupDiscardsOldProcessResources(t *testing.T) {
	r, cfg, body := runtimePendingFixture(t)
	old, release, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("missing lease")
	}
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml"), strings.Replace(body, "Original", "Latest", 1))
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	// Emulate process termination: no persisted Agent/Skill pins exist.
	release()
	rebuilt, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := rebuilt.AgentDefinition("first")
	if current.Name != "Latest" {
		t.Fatal("startup did not use current source")
	}
	if _, err := os.Stat(old.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("historical version survived startup")
	}
}

func TestPublishedAgentTreeIsReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; Windows requires target validation")
	}
	r, _, _ := runtimePendingFixture(t)
	def, release, ok := r.AcquireAgentRuntime("first")
	if !ok {
		t.Fatal("missing lease")
	}
	defer release()
	err := filepath.Walk(def.RuntimeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0222 != 0 {
			t.Errorf("writable published resource: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(filepath.Join(def.RuntimeDir, ".DS_Store"), []byte("noise"), 0600); err == nil {
			t.Fatal("published root accepted incidental file")
		}
	}
	if _, done, ok := r.AcquireAgentRuntime("first"); !ok {
		t.Fatal("sealed version failed integrity check")
	} else {
		done()
	}
}

func TestProvisionalLeaseProtectsVersionDuringOutOfLockVerification(t *testing.T) {
	r, cfg, body := runtimePendingFixture(t)
	old, _ := r.AgentDefinition("first")
	r.executionMu.Lock()
	release, checks := r.retainForVerificationLocked([]AgentDefinition{old})
	r.executionMu.Unlock()
	defer release()
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "first", "agent.yml"), strings.Replace(body, "Original", "New", 1))
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	latest, done, ok := r.AcquireAgentRuntime("first")
	if !ok || latest.RuntimeDir == old.RuntimeDir {
		t.Fatal("publication blocked by provisional lease")
	}
	defer done()
	if _, err := os.Stat(old.RuntimeDir); err != nil {
		t.Fatal("provisional version collected", err)
	}
	// The success path performs all tree I/O without needing executionMu.
	r.executionMu.Lock()
	verified := make(chan bool, 1)
	go func() { verified <- r.verifyRuntimeChecks(checks, release) }()
	select {
	case ok := <-verified:
		r.executionMu.Unlock()
		if !ok {
			t.Fatal("frozen version verification failed")
		}
	case <-time.After(5 * time.Second):
		r.executionMu.Unlock()
		<-verified
		t.Fatal("verification waits for global execution lock")
	}
	release()
	if _, err := os.Stat(old.RuntimeDir); !os.IsNotExist(err) {
		t.Fatal("provisional version not collected after release", err)
	}
}
