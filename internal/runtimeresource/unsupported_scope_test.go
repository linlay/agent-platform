package runtimeresource

import (
	"encoding/json"
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

// Construct journal fixtures with real backups and captured permissions.
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

func TestReadStateIgnoresRetiredTeamsRecords(t *testing.T) {
	for _, relative := range []string{
		"teams/README.md", "teams/legacy-team", "teams", "teams/", "teams/.", "teams/..",
		"teams/../outside", "teams/name/nested", "teams//README.md", `teams/name\nested`, "teams/name\x00bad",
	} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			statePath := writeScopeTestState(t, root, "previous-state", "0.4.38", []string{relative, "agents/current"})
			before := snapshotScopeTestTree(t, root)
			state, exists, err := readState(statePath)
			if err != nil || !exists || !reflect.DeepEqual(state.ManagedUnits, []string{"agents/current"}) {
				t.Fatalf("retired record must be ignored: %#v %v", state, err)
			}
			assertScopeTestTreeUnchanged(t, root, before)
		})
	}
}

func TestReadStateRejectsUnsafeCurrentOrUnknownManagedPaths(t *testing.T) {
	for _, relative := range []string{
		"/teams/README.md", `teams\README.md`, "teams-other/README.md", "Teams/README.md",
		"unknown/README.md", "agents/name/nested", "agents/..", "agents/../outside",
		"agents/", `agents/name\nested`, "agents/name\x00bad",
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

func TestSyncIgnoresTeamsStateForNoOpUpgradeAndManualImport(t *testing.T) {
	for _, scenario := range []struct{ name, mode, to string }{
		{"same version", ModeVersionChange, "0.4.32"},
		{"version upgrade", ModeVersionChange, "0.4.49"},
		{"manual import", ModeManualImport, "0.4.32"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			writeScopeTestResources(t, root)
			statePath := writeScopeTestState(t, root, "previous-state", "0.4.32", []string{"teams/README.md", "teams/../agents/current", "agents/current"})
			oldBackup := filepath.Join(root, stateDirectoryName, backupDirectory, "previous", "teams", "README.md")
			writeTestFile(t, oldBackup, "retained backup")
			beforeTeams := snapshotScopeTestTree(t, filepath.Join(root, "teams"))
			before := snapshotScopeTestTree(t, root)
			source := filepath.Join(t.TempDir(), "must-not-be-read.zip")
			noOp := scenario.name == "same version"
			if !noOp {
				source = writeTestZip(t, map[string]string{
					"env/VERSION":              "v" + scenario.to + "\n",
					"env/agents/new/notes.txt": "new current unit",
					"env/teams/README.md":      "must not overwrite",
				})
			}
			result, err := Sync(Options{
				RuntimeDir: root, Source: source,
				DesktopFrom: "0.4.32", DesktopTo: scenario.to, Mode: scenario.mode,
			})
			if err != nil || result.Changed == noOp {
				t.Fatalf("retired state blocked %s: %#v %v", scenario.name, result, err)
			}
			if noOp {
				assertScopeTestTreeUnchanged(t, root, before)
				return
			}
			assertScopeTestTreeUnchanged(t, filepath.Join(root, "teams"), beforeTeams)
			assertTestFile(t, oldBackup, "retained backup")
			assertMissing(t, filepath.Join(result.BackupDir, "teams"))
			assertMissing(t, filepath.Join(root, "agents", "current"))
			assertTestFile(t, filepath.Join(root, "agents", "new", "notes.txt"), "new current unit")
			data, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			var state State
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state.ManagedUnits, []string{"agents/new"}) || result.Stats.RemovedManagedUnits != 1 {
				t.Fatalf("current ownership lost or retired records retained: %#v %#v", state, result)
			}
		})
	}
}

func TestSyncIgnoresTeamsJournalDuringCleanupAndRollback(t *testing.T) {
	for _, phase := range []string{"committed", "unfinished"} {
		for _, recorded := range []string{"target", "permission only"} {
			t.Run(phase+"/"+recorded, func(t *testing.T) {
				root := t.TempDir()
				writeScopeTestResources(t, root)
				version, expectedAgent := "0.4.32", "original current agent"
				transactionID := "unfinished-transaction"
				stateTransactionID := "previous-state"
				if phase == "committed" {
					stateTransactionID = transactionID
					version, expectedAgent = "0.4.36", "partial current agent"
				}
				statePath := writeScopeTestState(t, root, stateTransactionID, version, []string{"teams/README.md", "agents/current"})
				targets := append(append([]string{}, resourceScopes...), "VERSION")
				permissions := append(append([]string{}, targets...), "teams")
				if recorded == "target" {
					targets = append(targets, "teams")
				}
				journalPath := writeScopeTestJournal(t, root, transactionID, targets, permissions)
				data, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				var journal upgradeJournal
				if err := json.Unmarshal(data, &journal); err != nil {
					t.Fatal(err)
				}
				// Even malformed retired records must never resolve into active paths.
				journal.Targets = append(journal.Targets, backupTarget{RelativePath: "teams/../agents"})
				journal.PathModes["teams/../agents/current/notes.txt"] = 0
				if err := atomicWriteJSON(journalPath, journal, 0o600); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(root, "teams", "README.md"), "current unmanaged README")
				writeTestFile(t, filepath.Join(root, "agents", "current", "notes.txt"), "partial current agent")
				writeTestFile(t, filepath.Join(root, "agents", "new", "notes.txt"), "partial addition")
				writeTestFile(t, filepath.Join(root, "VERSION"), "v0.4.36\n")
				beforeTeams := snapshotScopeTestTree(t, filepath.Join(root, "teams"))
				beforeState, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				result, err := Sync(Options{
					RuntimeDir: root, Source: filepath.Join(t.TempDir(), "must-not-be-read.zip"),
					DesktopFrom: "0.4.32", DesktopTo: version, Mode: ModeVersionChange,
				})
				if err != nil || result.Changed {
					t.Fatalf("retired records blocked %s recovery: %#v %v", phase, result, err)
				}
				assertScopeTestTreeUnchanged(t, filepath.Join(root, "teams"), beforeTeams)
				assertTestFile(t, statePath, string(beforeState))
				assertTestFile(t, filepath.Join(root, "agents", "current", "notes.txt"), expectedAgent)
				assertTestFile(t, filepath.Join(root, "VERSION"), "v"+version+"\n")
				assertTestFile(t, filepath.Join(root, stateDirectoryName, backupDirectory, "test-transaction", "teams", "README.md"), "original unmanaged README")
				assertMissing(t, journalPath)
			})
		}
	}
}

func TestSyncDoesNotReadRetiredTeamsTree(t *testing.T) {
	root := t.TempDir()
	// A non-directory would fail candidate construction if teams were inspected.
	writeTestFile(t, filepath.Join(root, "teams"), "opaque legacy data")
	writeScopeTestState(t, root, "previous-state", "0.4.38", []string{"teams/README.md"})
	result, err := Sync(Options{
		RuntimeDir: root, Source: writeTestZip(t, map[string]string{"env/VERSION": "v0.4.49\n"}),
		DesktopFrom: "0.4.38", DesktopTo: "0.4.49", Mode: ModeVersionChange,
	})
	if err != nil || !result.Changed {
		t.Fatalf("retired teams tree was inspected: %#v %v", result, err)
	}
	assertTestFile(t, filepath.Join(root, "teams"), "opaque legacy data")
	assertMissing(t, filepath.Join(result.BackupDir, "teams"))
}

func TestSyncRejectsUnsafeCurrentJournalRecords(t *testing.T) {
	for _, scenario := range []struct{ name, target, permission string }{
		{"nested target", "agents/current", ""},
		{"unknown target", "unknown", ""},
		{"escaping permission", "", "agents/../outside"},
		{"unknown permission", "", "unknown/data"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			writeScopeTestResources(t, root)
			writeScopeTestState(t, root, "previous-state", "0.4.32", []string{"teams/README.md", "agents/current"})
			scopes := append(append([]string{}, resourceScopes...), "VERSION")
			journalPath := writeScopeTestJournal(t, root, "unfinished-transaction", scopes, scopes)
			data, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			var journal upgradeJournal
			if err := json.Unmarshal(data, &journal); err != nil {
				t.Fatal(err)
			}
			category := "unsafe target"
			if scenario.target != "" {
				journal.Targets = append(journal.Targets, backupTarget{RelativePath: scenario.target})
			} else {
				journal.PathModes[scenario.permission] = 0o600
				category = "unsafe permission path"
			}
			if err := atomicWriteJSON(journalPath, journal, 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotScopeTestTree(t, root)
			result, err := Sync(Options{
				RuntimeDir: root, Source: filepath.Join(t.TempDir(), "must-not-be-read.zip"),
				DesktopFrom: "0.4.32", DesktopTo: "0.4.49", Mode: ModeVersionChange,
			})
			if err == nil || !strings.Contains(err.Error(), category) || result.Changed {
				t.Fatalf("unsafe current journal accepted: %#v %v", result, err)
			}
			assertScopeTestTreeUnchanged(t, root, before)
		})
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
