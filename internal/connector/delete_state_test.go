package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deletionFixture(t *testing.T) (Sources, map[string]string) {
	t.Helper()
	sources := Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors-center"), StateRoot: filepath.Join(t.TempDir(), "connectors")}
	files := map[string]string{
		filepath.Join(sources.ExternalRoot, "demo", "connector.json"): `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"self"}`,
		filepath.Join(sources.ExternalRoot, "demo", "cli.json"):       `{}`,
		filepath.Join(sources.StateRoot, "demo", "oauth.json"):        "private-oauth-credential",
		filepath.Join(sources.StateRoot, "demo", "config", "login"):   "private-cli-login",
		filepath.Join(sources.StateRoot, "demo", "data", "local"):     "private-cli-data",
		filepath.Join(sources.InstallRoot(), "demo", "active.json"):   "active-installation",
		filepath.Join(sources.InstallRoot(), "demo", "1.0.0", "bin"):  "managed-cli-executable",
	}
	for path, contents := range files {
		writeDeletionFixtureFile(t, path, contents)
	}
	return sources, files
}

func writeDeletionFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertDeletionFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, want := range files {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("lost original %s: %q %v", path, got, err)
		}
	}
}

func TestDeletePackagePurgesPrivateStateAndInstallations(t *testing.T) {
	sources, _ := deletionFixture(t)
	kept := map[string]string{
		filepath.Join(sources.StateRoot, "other", "oauth.json"):                    "other-connector-credential",
		filepath.Join(sources.InstallRoot(), "other", "active.json"):               "other-connector-installation",
		filepath.Join(filepath.Dir(sources.StateRoot), "identity", "access-token"): "desktop-sso",
	}
	for path, contents := range kept {
		writeDeletionFixtureFile(t, path, contents)
	}
	if err := WriteStateControl(sources.StateRoot, "demo", StateControl{Generation: "previous"}); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	err := DeletePackage(t.Context(), sources, "demo", nil, func() error {
		reloads++
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		release, err := AcquireStateLock(ctx, sources.StateRoot, "demo")
		if err != nil {
			t.Fatalf("reload ran under credential lock: %v", err)
		}
		defer release()
		control, err := ReadStateControl(sources.StateRoot, "demo")
		if err != nil || !control.Deleting {
			t.Fatalf("missing deletion fence during reload: %+v %v", control, err)
		}
		for _, root := range []string{sources.ExternalRoot, sources.StateRoot, sources.InstallRoot()} {
			if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
				t.Fatalf("target was not isolated before reload: %s %v", root, err)
			}
		}
		return nil
	})
	if err != nil || reloads != 1 {
		t.Fatalf("delete: %v, reloads=%d", err, reloads)
	}
	assertDeletionFiles(t, kept)
	for _, root := range []string{sources.ExternalRoot, sources.StateRoot, sources.InstallRoot()} {
		if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
			t.Fatalf("private directory survived: %s %v", root, err)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".connector-delete-") {
				t.Fatalf("deleted private data survived in staging: %s", entry.Name())
			}
		}
	}
	control, err := ReadStateControl(sources.StateRoot, "demo")
	if err != nil || !control.Deleted || control.Deleting || control.Generation == "previous" || control.Generation == "" {
		t.Fatalf("invalid committed fence: %+v %v", control, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sources.ExternalRoot), ".state")); !os.IsNotExist(err) {
		t.Fatalf("custom state root was ignored: %v", err)
	}
}

func TestDeletePackageRestoresEveryDirectoryAndControlOnReloadFailure(t *testing.T) {
	sources, files := deletionFixture(t)
	previous := StateControl{Generation: "previous-installation"}
	if err := WriteStateControl(sources.StateRoot, "demo", previous); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	err := DeletePackage(t.Context(), sources, "demo", nil, func() error {
		reloads++
		if reloads == 1 {
			return errors.New("reload rejected")
		}
		assertDeletionFiles(t, files)
		got, err := ReadStateControl(sources.StateRoot, "demo")
		if err != nil || got != previous {
			t.Fatalf("catalog recovered before credentials: %+v %v", got, err)
		}
		return nil
	})
	if !errors.Is(err, ErrDeleteReload) || reloads != 2 {
		t.Fatalf("rollback error=%v reloads=%d", err, reloads)
	}
	assertDeletionFiles(t, files)
}

func TestDeletePackageRollsBackEarlierMovesWhenLaterMoveFails(t *testing.T) {
	sources, files := deletionFixture(t)
	failure := errors.New("installation directory is in use")
	err := deletePackage(t.Context(), sources, "demo", nil, func() error {
		t.Fatal("failed staging reloaded catalog")
		return nil
	}, deletionFileOps{
		rename: func(from, to string) error {
			if from == filepath.Join(sources.InstallRoot(), "demo") {
				return failure
			}
			return os.Rename(from, to)
		},
		removeAll: os.RemoveAll,
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	assertDeletionFiles(t, files)
	control, err := ReadStateControl(sources.StateRoot, "demo")
	if err != nil || control != (StateControl{}) {
		t.Fatalf("staging failure retained fence: %+v %v", control, err)
	}
}

func TestDeletePackageCleanupFailureCanBeRetriedAndBlocksImport(t *testing.T) {
	sources, _ := deletionFixture(t)
	failure := errors.New("private files still open")
	err := deletePackage(t.Context(), sources, "demo", nil, nil, deletionFileOps{
		rename: os.Rename,
		removeAll: func(path string) error {
			if filepath.Dir(path) == sources.StateRoot {
				return failure
			}
			return os.RemoveAll(path)
		},
	})
	if !errors.Is(err, ErrDeleteCleanup) || !errors.Is(err, failure) {
		t.Fatalf("cleanup failure was hidden: %v", err)
	}
	control, err := ReadStateControl(sources.StateRoot, "demo")
	if err != nil || !control.Deleted {
		t.Fatalf("failed cleanup restored old credential access: %+v %v", control, err)
	}
	record, err := readDeletionRecord(sources, "demo")
	if err != nil || record == nil {
		t.Fatalf("missing retry receipt: %+v %v", record, err)
	}
	otherDeployment := sources
	otherDeployment.ExternalRoot = filepath.Join(t.TempDir(), "connectors-center")
	if err := DeletePackage(t.Context(), otherDeployment, "demo", nil, nil); err == nil {
		t.Fatal("cleanup with different runtime roots reported success")
	}
	data := archiveFixture(t, map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"self"}`, "cli.json": `{}`})
	if _, err := ImportArchive(t.Context(), sources, bytes.NewReader(data), int64(len(data)), false, nil, nil); !errors.Is(err, ErrDeleteCleanup) {
		t.Fatalf("import overtook pending cleanup: %v", err)
	}
	if err := DeletePackage(t.Context(), sources, "demo", nil, func() error {
		t.Fatal("cleanup retry reloaded already-deleted package")
		return nil
	}); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if pending, err := readDeletionRecord(sources, "demo"); err != nil || pending != nil {
		t.Fatalf("cleanup receipt survived: %+v %v", pending, err)
	}
	if err := DeletePackage(t.Context(), sources, "demo", nil, nil); !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("missing package after cleanup: %v", err)
	}
}

func TestDeletePackageMissingDoesNotRunReferenceCheck(t *testing.T) {
	sources := Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors-center")}
	if err := DeletePackage(t.Context(), sources, "missing", func(string) error {
		t.Fatal("missing package entered reference check")
		return nil
	}, nil); !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("missing package: %v", err)
	}
}

func TestDeletePackageRejectsPrivateStateAndInstallAliases(t *testing.T) {
	for _, kind := range []string{"state", "install", "state-root", "install-root"} {
		t.Run(kind, func(t *testing.T) {
			sources, files := deletionFixture(t)
			outside := t.TempDir()
			writeDeletionFixtureFile(t, filepath.Join(outside, "keep"), "outside-data")
			path := sources.StateRoot
			if strings.HasPrefix(kind, "install") {
				path = sources.InstallRoot()
			}
			if !strings.HasSuffix(kind, "-root") {
				path = filepath.Join(path, "demo")
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skip(err)
			}
			if err := DeletePackage(t.Context(), sources, "demo", nil, nil); err == nil {
				t.Fatal("deletion accepted aliased state/install path")
			}
			for file := range files {
				if strings.HasPrefix(file, sources.ExternalRoot+string(filepath.Separator)) {
					if _, err := os.Stat(file); err != nil {
						t.Fatal("invalid state deleted source package")
					}
				}
			}
			assertDeletionFiles(t, map[string]string{filepath.Join(outside, "keep"): "outside-data"})
		})
	}
}

func TestDeletePackageRejectsUnsafeCleanupReceiptAndStage(t *testing.T) {
	for _, unsafePath := range []bool{false, true} {
		t.Run(map[bool]string{false: "stage-alias", true: "receipt-traversal"}[unsafePath], func(t *testing.T) {
			sources, _ := deletionFixture(t)
			err := deletePackage(t.Context(), sources, "demo", nil, nil, deletionFileOps{
				rename: os.Rename,
				removeAll: func(string) error {
					return errors.New("files in use")
				},
			})
			if !errors.Is(err, ErrDeleteCleanup) {
				t.Fatal(err)
			}
			record, err := readDeletionRecord(sources, "demo")
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			writeDeletionFixtureFile(t, filepath.Join(outside, "keep"), "outside-data")
			if unsafePath {
				record.Stages[0].Name = "../" + filepath.Base(outside)
				path, err := deletionRecordPath(sources, "demo", false)
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				stage := record.Stages[0]
				path := filepath.Join(deletionStageBase(sources, stage.Kind), stage.Name)
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skip(err)
				}
			}
			if err := DeletePackage(t.Context(), sources, "demo", nil, nil); err == nil {
				t.Fatal("cleanup trusted unsafe persisted path")
			}
			assertDeletionFiles(t, map[string]string{filepath.Join(outside, "keep"): "outside-data"})
		})
	}
}

func TestReimportActivatesDeletedConnectorAndPreservesGeneration(t *testing.T) {
	for _, failReload := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[failReload], func(t *testing.T) {
			sources, _ := deletionFixture(t)
			if err := DeletePackage(t.Context(), sources, "demo", nil, nil); err != nil {
				t.Fatal(err)
			}
			deleted, err := ReadStateControl(sources.StateRoot, "demo")
			if err != nil {
				t.Fatal(err)
			}
			data := archiveFixture(t, map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"2.0.0","type":"cli","authMode":"self"}`, "cli.json": `{}`})
			firstReload := true
			_, err = ImportArchive(context.Background(), sources, bytes.NewReader(data), int64(len(data)), false, nil, func() error {
				if firstReload {
					firstReload = false
					control, err := ReadStateControl(sources.StateRoot, "demo")
					if err != nil || !control.Deleted || control.Deleting || control.Generation != deleted.Generation {
						t.Fatalf("publication activated lifecycle before reload committed: %+v %v", control, err)
					}
					// Model a different Manager's credential writer: it must be able
					// to take the stable lock, then reject authorization while reload
					// still owns this uncommitted package publication.
					writer := make(chan error, 1)
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					go func() {
						release, err := AcquireStateLock(ctx, sources.StateRoot, "demo")
						if err != nil {
							writer <- err
							return
						}
						defer release()
						control, err := ReadStateControl(sources.StateRoot, "demo")
						if err != nil {
							writer <- err
						} else if control.Deleted || control.Deleting {
							writer <- ErrBusy
						} else {
							writer <- os.Mkdir(filepath.Join(sources.StateRoot, "demo"), 0o700)
						}
					}()
					if err := <-writer; !errors.Is(err, ErrBusy) {
						t.Fatalf("concurrent authorization passed publication fence: %v", err)
					}
					if failReload {
						return errors.New("reload failed")
					}
				}
				return nil
			})
			if (err != nil) != failReload {
				t.Fatalf("import error: %v", err)
			}
			got, err := ReadStateControl(sources.StateRoot, "demo")
			if err != nil || got.Deleted != failReload || got.Generation != deleted.Generation {
				t.Fatalf("lifecycle mismatch after import: %+v %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(sources.StateRoot, "demo")); !os.IsNotExist(err) {
				t.Fatalf("reimport restored deleted private state: %v", err)
			}
		})
	}
}

func TestReimportRollsBackWhenActivationIsCanceledAfterReload(t *testing.T) {
	sources, _ := deletionFixture(t)
	if err := DeletePackage(t.Context(), sources, "demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	deleted, err := ReadStateControl(sources.StateRoot, "demo")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	data := archiveFixture(t, map[string]string{"connector.json": `{"id":"demo","name":"Demo","version":"2.0.0","type":"cli","authMode":"self"}`, "cli.json": `{}`})
	_, err = ImportArchive(ctx, sources, bytes.NewReader(data), int64(len(data)), false, nil, func() error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("activation did not fail with request: %v", err)
	}
	got, err := ReadStateControl(sources.StateRoot, "demo")
	if err != nil || got != deleted {
		t.Fatalf("activation failure changed deletion fence: %+v %v", got, err)
	}
	for _, root := range []string{sources.ExternalRoot, sources.StateRoot, sources.InstallRoot()} {
		if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
			t.Fatalf("activation failure left package/private state: %s %v", root, err)
		}
	}
}
