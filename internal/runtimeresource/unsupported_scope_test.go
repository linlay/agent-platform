package runtimeresource

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeScopeTestState(t *testing.T, root, transactionID, version string, units []string) string {
	t.Helper()
	statePath := filepath.Join(root, stateDirectoryName, stateFileName)
	if err := atomicWriteJSON(statePath, State{
		SchemaVersion: 1, TransactionID: transactionID, DesktopVersion: version,
		PackageSHA256: "test-package-sha", ManagedUnits: units, ManagedRegistryFiles: []string{},
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	return statePath
}

type scopeTestEntry struct {
	mode os.FileMode
	data string
}

func snapshotScopeTestTree(t *testing.T, root string) map[string]scopeTestEntry {
	t.Helper()
	entries := map[string]scopeTestEntry{}
	err := filepath.Walk(root, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		entry := scopeTestEntry{mode: info.Mode()}
		if !info.IsDir() {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			entry.data = string(data)
		}
		entries[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertScopeTestTreeUnchanged(t *testing.T, root string, before map[string]scopeTestEntry) {
	t.Helper()
	if !reflect.DeepEqual(snapshotScopeTestTree(t, root), before) {
		t.Fatal("rejection changed runtime files, permissions, state, journal or backups")
	}
}

func writeScopeTestResources(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "teams", "README.md"), "original unmanaged README")
	writeTestFile(t, filepath.Join(root, "teams", "legacy-team", "notes.txt"), "original unmanaged team")
	writeTestFile(t, filepath.Join(root, "agents", "current", "notes.txt"), "original current agent")
	writeTestFile(t, filepath.Join(root, "VERSION"), "v0.4.32\n")
	if os.PathSeparator == '/' {
		if err := os.Chmod(filepath.Join(root, "teams", "README.md"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

// Construct journal fixtures with real backups and captured permissions. The
// caller chooses the recorded scopes; unsupported records must never recover.
func writeScopeTestJournal(t *testing.T, root, transactionID string, targets, permissionScopes []string) string {
	t.Helper()
	backupDir := filepath.Join(root, stateDirectoryName, backupDirectory, "test-transaction")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	modes, err := capturePathModes(root, permissionScopes)
	if err != nil {
		t.Fatal(err)
	}
	journal := upgradeJournal{
		SchemaVersion: 1, TransactionID: transactionID, RuntimeDir: root,
		DesktopFrom: "0.4.32", DesktopTo: "0.4.36", Mode: ModeVersionChange,
		BackupDir: backupDir, PathModes: modes,
	}
	for _, relative := range permissionScopes {
		if _, err := copyPathIfExists(filepath.Join(root, relative), filepath.Join(backupDir, relative)); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range targets {
		_, err := os.Lstat(filepath.Join(root, relative))
		journal.Targets = append(journal.Targets, backupTarget{RelativePath: relative, Existed: err == nil})
	}
	if err := securePrivateTree(backupDir); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, stateDirectoryName, journalFileName)
	if err := atomicWriteJSON(journalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	return journalPath
}

func TestReadStateRejectsUnsupportedManagedScopes(t *testing.T) {
	for _, relative := range []string{
		"teams/README.md", "teams/legacy-team", "teams", "teams/", "teams/.", "teams/..",
		"teams/../outside", "teams/name/nested", "teams//README.md", "/teams/README.md",
		`teams\README.md`, `teams/name\nested`, "teams/name\x00bad", "teams-other/README.md",
		"Teams/README.md", "unknown/README.md", "agents/name/nested",
	} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			statePath := writeScopeTestState(t, root, "unsupported-state", "0.4.32", []string{relative})
			before := snapshotScopeTestTree(t, root)
			if _, _, err := readState(statePath); err == nil || !strings.Contains(err.Error(), "state is unsafe") {
				t.Fatalf("unsupported managed path %q accepted or misreported: %v", relative, err)
			}
			assertScopeTestTreeUnchanged(t, root, before)
		})
	}
}

func TestSyncRejectsUnsupportedTeamsStateBeforeNoOpOrPublishing(t *testing.T) {
	for _, scenario := range []struct{ name, mode, to string }{
		{"same version", ModeVersionChange, "0.4.32"},
		{"version upgrade", ModeVersionChange, "0.4.45"},
		{"manual import", ModeManualImport, "0.4.32"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			writeScopeTestResources(t, root)
			writeScopeTestState(t, root, "unsupported-state", "0.4.32", []string{"teams/README.md", "agents/current"})
			writeTestFile(t, filepath.Join(root, stateDirectoryName, backupDirectory, "previous", "teams", "README.md"), "retained backup")
			before := snapshotScopeTestTree(t, root)
			published := false
			result, err := Sync(Options{
				RuntimeDir: root, Source: filepath.Join(t.TempDir(), "must-not-be-read.zip"),
				DesktopFrom: "0.4.32", DesktopTo: scenario.to, Mode: scenario.mode,
				BeforePublish: func() error { published = true; return nil },
			})
			if err == nil || !strings.Contains(err.Error(), "state is unsafe") || !strings.Contains(err.Error(), "teams/README.md") {
				t.Fatalf("unsupported state must fail before reading the source: %#v %v", result, err)
			}
			if result.Changed || published {
				t.Fatalf("unsupported state reached publication: %#v", result)
			}
			assertScopeTestTreeUnchanged(t, root, before)
		})
	}
}

func TestSyncRejectsUnsupportedTeamsJournalWithoutCleanupOrRollback(t *testing.T) {
	for _, phase := range []string{"committed", "unfinished"} {
		for _, recorded := range []string{"target", "permission only"} {
			t.Run(phase+"/"+recorded, func(t *testing.T) {
				root := t.TempDir()
				writeScopeTestResources(t, root)
				// A valid current state isolates journal validation from state rejection.
				writeScopeTestState(t, root, "completed-state", "0.4.32", []string{"agents/current"})
				transactionID := "unfinished-transaction"
				if phase == "committed" {
					transactionID = "completed-state"
				}
				targets := append(append([]string{}, resourceScopes...), "VERSION")
				permissions := append(append([]string{}, targets...), "teams")
				if recorded == "target" {
					targets = append(targets, "teams")
				}
				writeScopeTestJournal(t, root, transactionID, targets, permissions)
				writeTestFile(t, filepath.Join(root, "teams", "README.md"), "current unmanaged README")
				writeTestFile(t, filepath.Join(root, "agents", "current", "notes.txt"), "partial current agent")
				writeTestFile(t, filepath.Join(root, "agents", "new", "notes.txt"), "partial addition")
				writeTestFile(t, filepath.Join(root, "VERSION"), "v0.4.36\n")
				before := snapshotScopeTestTree(t, root)
				result, err := Sync(Options{
					RuntimeDir: root, Source: filepath.Join(t.TempDir(), "must-not-be-read.zip"),
					DesktopFrom: "0.4.32", DesktopTo: "0.4.32", Mode: ModeVersionChange,
				})
				category := "unsafe target"
				if recorded == "permission only" {
					category = "unsafe permission path"
				}
				if err == nil || !strings.Contains(err.Error(), category) || !strings.Contains(err.Error(), "teams") || result.Changed {
					t.Fatalf("unsupported %s journal must be rejected before recovery: %#v %v", phase, result, err)
				}
				assertScopeTestTreeUnchanged(t, root, before)
			})
		}
	}
}

func TestSyncPreservesUnmanagedTeamsWithoutImportingOrBackingThemUp(t *testing.T) {
	root := t.TempDir()
	writeScopeTestResources(t, root)
	before := snapshotScopeTestTree(t, filepath.Join(root, "teams"))
	writeScopeTestState(t, root, "current-state", "0.4.32", []string{"agents/current"})
	result, err := Sync(Options{
		RuntimeDir: root, Source: writeTestZip(t, map[string]string{
			"env/VERSION": "v0.4.45\n", "env/agents/new/notes.txt": "new current unit",
			"env/teams/README.md": "must not overwrite", "env/teams/new-team/data.txt": "must not import",
		}),
		DesktopFrom: "0.4.32", DesktopTo: "0.4.45", Mode: ModeVersionChange,
	})
	if err != nil || !result.Changed {
		t.Fatalf("current managed scope upgrade: %#v %v", result, err)
	}
	assertScopeTestTreeUnchanged(t, filepath.Join(root, "teams"), before)
	assertMissing(t, filepath.Join(result.BackupDir, "teams"))
	assertMissing(t, filepath.Join(root, "agents", "current"))
	assertTestFile(t, filepath.Join(root, "agents", "new", "notes.txt"), "new current unit")
	if result.Stats.RemovedManagedUnits != 1 || !reflect.DeepEqual(readTestState(t, root).ManagedUnits, []string{"agents/new"}) {
		t.Fatalf("unmanaged teams entered current ownership: %#v", result)
	}
}

func TestCurrentScopeJournalRecoveryStillSucceeds(t *testing.T) {
	for _, phase := range []string{"committed", "unfinished"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			writeScopeTestResources(t, root)
			writeScopeTestState(t, root, "previous-state", "0.4.32", []string{"agents/current"})
			scopes := append(append([]string{}, resourceScopes...), "VERSION")
			journalPath := writeScopeTestJournal(t, root, "current-transaction", scopes, scopes)
			writeTestFile(t, filepath.Join(root, "agents", "current", "notes.txt"), "published current agent")
			writeTestFile(t, filepath.Join(root, "VERSION"), "v0.4.36\n")
			version, expectedAgent := "0.4.32", "original current agent"
			if phase == "committed" {
				version, expectedAgent = "0.4.36", "published current agent"
				writeScopeTestState(t, root, "current-transaction", version, []string{"agents/current"})
			}
			statePath := filepath.Join(root, stateDirectoryName, stateFileName)
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Sync(Options{
				RuntimeDir: root, Source: filepath.Join(t.TempDir(), "must-not-be-read.zip"),
				DesktopFrom: "0.4.32", DesktopTo: version, Mode: ModeVersionChange,
			})
			if err != nil || result.Changed {
				t.Fatalf("current %s journal rejected: %#v %v", phase, result, err)
			}
			assertTestFile(t, filepath.Join(root, "agents", "current", "notes.txt"), expectedAgent)
			assertTestFile(t, filepath.Join(root, "VERSION"), "v"+version+"\n")
			assertTestFile(t, filepath.Join(root, "teams", "README.md"), "original unmanaged README")
			assertTestFile(t, statePath, string(before))
			assertTestFile(t, filepath.Join(root, stateDirectoryName, backupDirectory, "test-transaction", "agents", "current", "notes.txt"), "original current agent")
			assertMissing(t, journalPath)
		})
	}
}
