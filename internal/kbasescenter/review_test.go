package kbasescenter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTemplateIsNotALibrary(t *testing.T) {
	s := newStorageService(t, testEngine{})
	template := filepath.Join(s.root, "example")
	if err := os.Mkdir(template, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(template, "library.example.yml")
	const content = "name: \"Template\"\n"
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("example"); !errors.Is(err, ErrNotFound) {
		t.Fatal("GET accepted template", err)
	}
	if _, err := s.Edit("example", Input{Name: "Oops", SourcePath: t.TempDir()}); !errors.Is(err, ErrNotFound) {
		t.Fatal("PUT accepted template", err)
	}
	if _, err := s.Refresh("example"); !errors.Is(err, ErrNotFound) {
		t.Fatal("refresh accepted template", err)
	}
	if err := s.Delete("example"); !errors.Is(err, ErrNotFound) {
		t.Fatal("DELETE accepted template", err)
	}
	// Even a runtime orphan with the same ID must not turn the template into a live config.
	if _, err := s.runtimeDirectory("example", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("example"); err != nil {
		t.Fatal("orphan deletion", err)
	}
	actual, err := os.ReadFile(file)
	if err != nil || string(actual) != content {
		t.Fatalf("template changed: %s %v", actual, err)
	}
	if _, err := os.Stat(filepath.Join(template, "library.yml")); !os.IsNotExist(err) {
		t.Fatal("template promoted")
	}
}

func TestOfflineSourcesKeepCompletedIndexReadable(t *testing.T) {
	for _, withAlias := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "handwritten-alias"}[withAlias], func(t *testing.T) {
			s := newStorageService(t, testEngine{})
			d := createFixture(t, s)
			source := d.Collections[0].SourcePath
			if withAlias {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal(err)
				}
				d.Collections[0].SourcePath = alias
				writeFixture(t, s, d)
			}
			if _, err := s.Refresh(d.ID); err != nil {
				t.Fatal(err)
			}
			ready := waitState(t, s, d.ID, "ready")
			if err := os.Rename(source, source+"-offline"); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(context.Background(), s.root, s.runtimeRoot, testEngine{})
			if err != nil {
				t.Fatal(err)
			}
			offline, err := reopened.Get(d.ID)
			if err != nil || offline.State != "ready" || offline.IndexedAt != ready.IndexedAt || len(offline.SourceWarnings) != 1 || !strings.Contains(offline.Error, "unavailable") {
				t.Fatalf("offline status: %+v %v", offline, err)
			}
			for _, operation := range []string{"search", "files", "read"} {
				arg := "fixture"
				limit := 10
				if operation == "read" {
					arg = "kbx://docs/note.md"
				}
				if _, err := reopened.Read(context.Background(), d.ID, operation, arg, limit); err != nil {
					t.Fatalf("offline %s: %v", operation, err)
				}
			}
			if _, err := reopened.Refresh(d.ID); err == nil {
				t.Fatal("refreshed offline source")
			}
			if _, err := reopened.Search(context.Background(), d.ID, SearchInput{Query: "fixture"}); err != nil {
				t.Fatal("preflight failure revoked readable index", err)
			}
			d.Collections[0].SourcePath = filepath.Join(t.TempDir(), "other-offline-source")
			writeFixture(t, reopened, d)
			changed, err := reopened.Get(d.ID)
			if err != nil || changed.State != "unindexed" {
				t.Fatalf("offline scope change: %+v %v", changed, err)
			}
			if _, err := reopened.Search(context.Background(), d.ID, SearchInput{Query: "fixture"}); err == nil {
				t.Fatal("read another offline scope")
			}
		})
	}
}

func TestOfflineRetargetedSymlinkCannotBorrowSnapshot(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(d.Collections[0].SourcePath, alias); err != nil {
		t.Fatal(err)
	}
	d.Collections[0].SourcePath = alias
	writeFixture(t, s, d)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "different-offline-source"), alias); err != nil {
		t.Fatal(err)
	}
	current, err := s.Get(d.ID)
	if err != nil || current.State != "unindexed" {
		t.Fatalf("retargeted link reused snapshot: %+v %v", current, err)
	}
}

func TestFailedRefreshCannotExposePartiallyUpdatedIndex(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	s.engine = testEngine{err: errors.New("embedding timed out after update")}
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "error")
	if _, err := s.Search(context.Background(), d.ID, SearchInput{Query: "fixture"}); err == nil {
		t.Fatal("read failed in-place update")
	}
	state, err := s.readState(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.State = "indexing"
	if err := s.saveState(d.ID, state); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), s.root, s.runtimeRoot, testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.Get(d.ID)
	if err != nil || current.State != "error" || !strings.Contains(current.Error, "interrupted") {
		t.Fatalf("restart: %+v %v", current, err)
	}
	if _, err := reopened.Search(context.Background(), d.ID, SearchInput{Query: "fixture"}); err == nil {
		t.Fatal("read interrupted update")
	}
}

func TestRefreshRecoversCorruptStateButRejectsSymlink(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	statePath := filepath.Join(s.runtimeRoot, "libraries", d.ID, "state.json")
	for _, content := range []string{"{broken", "null", "{}", "{\"state\":\"unknown\"}"} {
		if err := os.WriteFile(statePath, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Refresh(d.ID); err != nil {
			t.Fatal("could not repair corrupt state", err)
		}
		waitState(t, s, d.ID, "ready")
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(d.ID); err == nil {
		t.Fatal("treated symlink as corrupt JSON")
	}
	actual, err := os.ReadFile(target)
	if err != nil || string(actual) != "secret" {
		t.Fatal("changed symlink target", err)
	}
}

func TestQuarantinedIndexIsUnreadableBeforeCleanup(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	if _, err := s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "ready")
	// Simulate process death between rename and recursive cleanup.
	trash, err := quarantineDirectory(filepath.Join(s.runtimeRoot, "libraries", d.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(trash, "state.json")); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Get(d.ID)
	if err != nil || loaded.State != "unindexed" {
		t.Fatalf("quarantined index exposed: %+v %v", loaded, err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("trash listed: %+v %v", list, err)
	}
	if _, err := s.Search(context.Background(), d.ID, SearchInput{Query: "fixture"}); err == nil {
		t.Fatal("read quarantined index")
	}
}

func TestCreateRollsBackWhenRuntimePreparationFails(t *testing.T) {
	s := newStorageService(t, testEngine{})
	libraries := filepath.Join(s.runtimeRoot, "libraries")
	if err := os.Remove(libraries); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libraries, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Input{Name: "New", SourcePath: t.TempDir()}); err == nil {
		t.Fatal("created without runtime state")
	}
	entries, err := os.ReadDir(s.root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("creation left config: %v %v", entries, err)
	}
}

func TestHandwrittenConfigurationDiagnostics(t *testing.T) {
	s := newStorageService(t, testEngine{})
	d := createFixture(t, s)
	path := filepath.Join(s.root, d.ID, "library.yml")
	for _, tc := range []struct{ content, want string }{
		{"name: 2024\n", "double quotes"},
		{"name: \"Demo\"\ncollections:\n  - name: 123\n    sourcePath: \"/tmp\"\n", "double quotes"},
		{"name: \"Demo\"\ncollections: [{name: docs, sourcePath: /tmp}]\n", "block list"},
	} {
		if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		item, err := s.Get(d.ID)
		if err != nil || !strings.Contains(item.Error, tc.want) {
			t.Fatalf("diagnostic: %+v %v", item, err)
		}
	}
	writeFixture(t, s, d)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "name: \"Docs\"", "name: 'it''s'", 1))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	item, err := s.Get(d.ID)
	if err != nil || item.Name != "it's" {
		t.Fatalf("single quote: %+v %v", item, err)
	}
	if err := os.Rename(filepath.Join(s.root, d.ID), filepath.Join(s.root, "Project-Docs")); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID == "Project-Docs" {
			found = true
			if !strings.Contains(item.Error, "lowercase") {
				t.Fatal(item)
			}
		}
	}
	if !found {
		t.Fatal("uppercase ID silently ignored")
	}
}

func TestPlatformCasePolicyForScopeAndRootChecks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive platform policy")
	}
	s := newStorageService(t, testEngine{})
	source := t.TempDir()
	collections := []Collection{{Name: "docs", SourcePath: source}, {Name: "copy", SourcePath: strings.ToUpper(source)}}
	if err := validateCollections(collections); err == nil {
		t.Fatal("case alias accepted as distinct source")
	}
	if !overlaps(strings.ToUpper(s.root), s.root) || !overlaps(strings.ToUpper(s.runtimeRoot), filepath.Join(s.runtimeRoot, "libraries")) {
		t.Fatal("case alias bypassed overlap")
	}
	if scopeFingerprint(collections[:1]) != scopeFingerprint([]Collection{{Name: "docs", SourcePath: strings.ToUpper(source)}}) {
		t.Fatal("path casing changed fingerprint")
	}
}
