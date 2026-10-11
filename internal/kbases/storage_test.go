package kbases

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStorageService(t *testing.T, engine Engine) *Service {
	t.Helper()
	root := t.TempDir()
	s, err := New(context.Background(), filepath.Join(root, "kbases"), filepath.Join(root, "ru-kbases"), engine)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func createFixture(t *testing.T, s *Service) Definition {
	t.Helper()
	d, err := s.Create(Input{Name: "Docs", Collections: []Collection{{Name: "docs", SourcePath: t.TempDir()}}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func writeFixture(t *testing.T, s *Service, d Definition) {
	t.Helper()
	if err := s.saveConfiguration(d); err != nil {
		t.Fatal(err)
	}
}
func waitIdle(t *testing.T, s *Service, id string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		s.mu.RLock()
		busy := s.busy[id]
		s.mu.RUnlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("indexing did not stop")
}

func TestConfigurationRuntimeSeparationAndRebuild(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	path := filepath.Join(s.root, d.ID, "library.yml")
	original, _ := os.ReadFile(path)
	for _, field := range []string{"state:", "id:", "indexedAt:", "createdAt:"} {
		if strings.Contains(string(original), field) {
			t.Fatalf("runtime field leaked: %s", original)
		}
	}
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	current, _ := os.ReadFile(path)
	if string(original) != string(current) {
		t.Fatal("refresh changed configuration")
	}
	if _, err := os.Stat(filepath.Join(s.runtimeRoot, d.ID, "index.sqlite")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.root, d.ID, "index.sqlite")); !os.IsNotExist(err) {
		t.Fatal("index stored in configuration directory")
	}
	reopened, err := New(context.Background(), s.root, s.runtimeRoot, testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded, err := reopened.Get(d.ID); err != nil || loaded.State != "ready" {
		t.Fatalf("restart lost index: %+v %v", loaded, err)
	}
	// Even deleting the entire runtime root is recoverable without touching configuration.
	if err := os.RemoveAll(s.runtimeRoot); err != nil {
		t.Fatal(err)
	}
	if loaded, err := s.Get(d.ID); err != nil || loaded.State != "unindexed" {
		t.Fatalf("missing runtime: %+v %v", loaded, err)
	}
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
}

func TestRefreshUsesFrozenScopeAndPreservesHandEdits(t *testing.T) {
	release := make(chan struct{})
	s := newStorageService(t, testEngine{release: release})
	d := createFixture(t, s)
	oldFingerprint := scopeFingerprint(d.Collections)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	d.Name = "Hand edited"
	d.Collections = []Collection{{Name: "replacement", SourcePath: t.TempDir()}}
	writeFixture(t, s, d)
	path := filepath.Join(s.root, d.ID, "library.yml")
	edited, _ := os.ReadFile(path)
	close(release)
	waitIdle(t, s, d.ID)
	actual, _ := os.ReadFile(path)
	if string(actual) != string(edited) {
		t.Fatal("background indexing overwrote hand edit")
	}
	loaded, err := s.Get(d.ID)
	if err != nil || loaded.Name != d.Name || loaded.State != "unindexed" || loaded.IndexedAt != 0 {
		t.Fatalf("stale scope exposed: %+v %v", loaded, err)
	}
	state, err := s.readState(d.ID)
	if err != nil || state.AppliedFingerprint != oldFingerprint {
		t.Fatalf("task fingerprint not frozen: %+v %v", state, err)
	}
	if _, err := s.Read(context.Background(), d.ID, "files", "", 0); err == nil {
		t.Fatal("read stale scope")
	}
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
}

func TestManualMetadataAndCanonicalSource(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	d.Collections = append(d.Collections, Collection{Name: "notes", SourcePath: t.TempDir()})
	writeFixture(t, s, d)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	ready := waitState(t, s, d.ID, "ready")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(d.Collections[0].SourcePath, alias); err != nil {
		t.Fatal(err)
	}
	d.Collections[0].SourcePath = alias
	d.Collections[0], d.Collections[1] = d.Collections[1], d.Collections[0]
	d.Name, d.Description = "Edited", "Only metadata"
	writeFixture(t, s, d)
	loaded, err := s.Get(d.ID)
	if err != nil || loaded.State != "ready" || loaded.IndexedAt != ready.IndexedAt || loaded.Collections[1].SourcePath == alias {
		t.Fatalf("canonical/reorder: %+v %v", loaded, err)
	}
	// A renamed collection is an index identity change.
	d.Collections[0].Name = "renamed"
	writeFixture(t, s, d)
	loaded, err = s.Get(d.ID)
	if err != nil || loaded.State != "unindexed" {
		t.Fatalf("name change: %+v %v", loaded, err)
	}
}

func TestBadConfigurationIsolationAndSlug(t *testing.T) {
	s := newStorageService(t, testEngine{})
	good := createFixture(t, s)
	bad := createFixture(t, s)
	for _, content := range []string{
		"name: [broken\n", "name: one\nname: two\n", "name: Demo\nstate: ready\n", "id: forbidden\nname: Demo\n",
	} {
		if err := os.WriteFile(filepath.Join(s.root, bad.ID, "library.yml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		list, err := s.List()
		if err != nil || len(list) != 2 {
			t.Fatalf("bad library poisoned list: %+v %v", list, err)
		}
		for _, d := range list {
			if d.ID == bad.ID && (d.State != "error" || d.Error == "") {
				t.Fatalf("missing diagnostic: %+v", d)
			}
		}
		if _, err := s.Refresh(bad.ID); err == nil {
			t.Fatal("indexed bad configuration")
		}
	}
	if err := s.Delete(bad.ID); err != nil {
		t.Fatal("cannot delete bad configuration", err)
	}
	if err := os.Rename(filepath.Join(s.root, good.ID), filepath.Join(s.root, "team-docs")); err != nil {
		t.Fatal(err)
	}
	d, err := s.Get("team-docs")
	if err != nil || d.ID != "team-docs" || d.State != "unindexed" {
		t.Fatalf("slug: %+v %v", d, err)
	}
	if err := os.Mkdir(filepath.Join(s.root, "example"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "example", "library.example.yml"), []byte("name: Template"), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("expected slug and old orphan, no template: %+v %v", list, err)
	}
}

func TestOrphansAndExplicitDeletion(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	if err := os.RemoveAll(filepath.Join(s.root, d.ID)); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || !list[0].Orphaned || list[0].State != "error" {
		t.Fatalf("orphan not reported: %+v %v", list, err)
	}
	runDir := filepath.Join(s.runtimeRoot, d.ID)
	if _, err := os.Stat(filepath.Join(runDir, "index.sqlite")); err != nil {
		t.Fatal("orphan cleaned automatically", err)
	}
	if err := s.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatal("explicit delete retained runtime")
	}
	if _, err := os.Stat(d.Collections[0].SourcePath); err != nil {
		t.Fatal("source deleted", err)
	}
}

func TestBothRootsAndRuntimeSymlinksAreProtected(t *testing.T) {
	s := newStorageService(t, testEngine{})
	for _, root := range []string{s.root, s.runtimeRoot, filepath.Join(s.runtimeRoot, "libraries"), filepath.Dir(s.runtimeRoot)} {
		if _, err := s.Create(Input{Name: "Unsafe", SourcePath: root}); err == nil {
			t.Fatalf("overlap accepted: %s", root)
		}
	}
	d := createFixture(t, s)
	runDir := filepath.Join(s.runtimeRoot, d.ID)
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, runDir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(d.ID); err == nil {
		t.Fatal("followed runtime symlink")
	}
	if err := s.Delete(d.ID); err == nil {
		t.Fatal("deleted through runtime symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("runtime target removed")
	}
}

func TestLegacyJSONIsRejectedAndYAMLStringsRoundTrip(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	d.Name = "Name # \"quoted\""
	d.Description = "Literal ${HOME}\n第二行 \\ test"
	writeFixture(t, s, d)
	actual, err := s.Get(d.ID)
	if err != nil || actual.Name != d.Name || actual.Description != d.Description {
		t.Fatalf("round trip: %+v %v", actual, err)
	}
	if err := os.Rename(filepath.Join(s.root, d.ID, "library.yml"), filepath.Join(s.root, d.ID, "library.json")); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || !strings.Contains(list[0].Error, "legacy library.json is unsupported") {
		t.Fatalf("legacy accepted: %+v %v", list, err)
	}
}

func TestAdminRepairsBadConfiguration(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	if err := os.WriteFile(filepath.Join(s.root, d.ID, "library.yml"), []byte("name: [broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit(d.ID, Input{Name: "Incomplete repair"}); err == nil {
		t.Fatal("metadata-only repair lost collections")
	}
	fixed, err := s.Edit(d.ID, Input{Name: "Repaired", Collections: d.Collections})
	if err != nil || fixed.State != "unindexed" || fixed.Name != "Repaired" {
		t.Fatalf("repair failed: %+v %v", fixed, err)
	}
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
}

func TestMissingStateAndDatabaseCannotExposeReadyIndex(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	for _, name := range []string{"state.json", "index.sqlite"} {
		if _, err := s.Refresh(d.ID); err != nil {
			t.Fatal(err)
		}
		waitState(t, s, d.ID, "ready")
		if err := os.Remove(filepath.Join(s.runtimeRoot, d.ID, name)); err != nil {
			t.Fatal(err)
		}
		loaded, err := s.Get(d.ID)
		if err != nil || loaded.State != "unindexed" || loaded.IndexedAt != 0 {
			t.Fatalf("missing %s: %+v %v", name, loaded, err)
		}
		if _, err := s.Read(context.Background(), d.ID, "files", "", 0); err == nil {
			t.Fatal("exposed incomplete runtime")
		}
	}
}
