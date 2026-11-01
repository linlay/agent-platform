package webapp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func fixtureService(t *testing.T) (*Service, Options) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose storage; Windows compilation remains covered separately")
	}
	root := t.TempDir()
	options := Options{Root: filepath.Join(root, "webapps"), DataRoot: filepath.Join(root, "webapp-data"), Config: config.DefaultWebAppsConfig()}
	s, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = removeTree(root) })
	return s, options
}
func packageBytes(t *testing.T, version string, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	if files == nil {
		files = map[string][]byte{}
	}
	if _, exists := files["webapp-0123456789abcdef/webapp.json"]; !exists {
		files["webapp-0123456789abcdef/webapp.json"] = []byte(`{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"test-app","label":"Test app","version":"` + version + `","target":"linux-x64","frontend":{"root":"frontend","index":"index.html"}}`)
	}
	if _, exists := files["webapp-0123456789abcdef/frontend/index.html"]; !exists {
		files["webapp-0123456789abcdef/frontend/index.html"] = []byte("<h1>" + version + "</h1>")
	}
	for name, data := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func uploadPackage(t *testing.T, s *Service, version string) Upload {
	t.Helper()
	u, err := s.Upload(context.Background(), bytes.NewReader(packageBytes(t, version, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func installPackage(t *testing.T, s *Service, version, revision string) Entry {
	t.Helper()
	u := uploadPackage(t, s, version)
	entry, err := s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	got, _ := ErrorCode(err)
	if err == nil || got != code {
		t.Fatalf("want %s got %v", code, err)
	}
}

func TestInstallPreflightAndDurableReopen(t *testing.T) {
	s, options := fixtureService(t)
	u := uploadPackage(t, s, "1.0.0")
	review, err := s.Preflight(context.Background(), u.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Manifest.ID != "webapp-0123456789abcdef" || review.FileCount != 2 || len(s.List()) != 0 {
		t.Fatal("preflight installed or returned wrong package")
	}
	entry, err := s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if entry.DesiredState != "stopped" || entry.ObservedState != "stopped" || entry.Version != "1.0.0" {
		t.Fatalf("%+v", entry)
	}
	if _, err := Open(options); err == nil {
		t.Fatal("second service acquired an existing instance")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(entry.ID)
	if err != nil || got.Revision != entry.Revision {
		t.Fatalf("reopen %+v %v", got, err)
	}
}
func TestInstallRejectsChangedUploadAndStaleRevision(t *testing.T) {
	s, _ := fixtureService(t)
	u := uploadPackage(t, s, "1.0.0")
	_, err := s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: strings.Repeat("0", 64)})
	requireCode(t, err, "webapp_upload_changed")
	f, err := os.OpenFile(filepath.Join(s.uploads(), u.UploadID, "package.zip"), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("corrupted"))
	f.Close()
	_, err = s.Preflight(context.Background(), u.UploadID)
	requireCode(t, err, "webapp_upload_changed")
	old := installPackage(t, s, "1.0.0", "")
	u = uploadPackage(t, s, "2.0.0")
	_, err = s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: "stale"})
	requireCode(t, err, "webapp_revision_conflict")
	installationID := s.records[old.ID].InstallationID
	updated, err := s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256, ExpectedRevision: old.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "2.0.0" || installationID != s.records[old.ID].InstallationID {
		t.Fatal("update lost identity")
	}
	changed, err := s.Upload(context.Background(), bytes.NewReader(packageBytes(t, "2.0.0", map[string][]byte{"webapp-0123456789abcdef/frontend/index.html": []byte("changed without version bump")})))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Install(context.Background(), InstallRequest{UploadID: changed.UploadID, SHA256: changed.SHA256, ExpectedRevision: updated.Revision})
	requireCode(t, err, "webapp_version_content_conflict")
}
func TestInstallConcurrentReviewsCannotOverwriteEachOther(t *testing.T) {
	s, _ := fixtureService(t)
	a := uploadPackage(t, s, "1.0.0")
	b := uploadPackage(t, s, "2.0.0")
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, u := range []Upload{a, b} {
		wg.Add(1)
		go func(u Upload) {
			defer wg.Done()
			_, err := s.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256})
			errs <- err
		}(u)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			requireCode(t, err, "webapp_revision_conflict")
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent install was not atomic")
	}
}
func TestArchiveRejectsUnsafeEntriesAndNativeCodeWithoutInstalling(t *testing.T) {
	for name, files := range map[string]map[string][]byte{
		"traversal": {"../escape": []byte("bad")}, "backslash": {"webapp-0123456789abcdef/frontend\\file": []byte("bad")}, "collision": {"webapp-0123456789abcdef/frontend/INDEX.HTML": []byte("bad")},
		"addon": {"webapp-0123456789abcdef/backend/addon.node": []byte("bad")}, "native-magic": {"webapp-0123456789abcdef/backend/run": {0x7f, 'E', 'L', 'F'}}, "secret": {"webapp-0123456789abcdef/.env": []byte("secret")},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixtureService(t)
			u, err := s.Upload(context.Background(), bytes.NewReader(packageBytes(t, "1.0.0", files)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Preflight(context.Background(), u.UploadID); err == nil {
				t.Fatal("unsafe package accepted")
			}
			if len(s.List()) != 0 {
				t.Fatal("unsafe package installed")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		s, _ := fixtureService(t)
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: "webapp-0123456789abcdef/link"}
		h.SetMode(os.ModeSymlink | 0777)
		f, _ := z.CreateHeader(h)
		_, _ = f.Write([]byte("/etc/passwd"))
		z.Close()
		u, _ := s.Upload(context.Background(), bytes.NewReader(b.Bytes()))
		if _, err := s.Preflight(context.Background(), u.UploadID); err == nil {
			t.Fatal("link accepted")
		}
	})
}
func TestInterruptedUpdateRestoresOriginalRecordAndCode(t *testing.T) {
	for stage := 0; stage <= 3; stage++ {
		t.Run(string(rune('0'+stage)), func(t *testing.T) {
			s, options := fixtureService(t)
			old := installPackage(t, s, "1.0.0", "")
			u := uploadPackage(t, s, "2.0.0")
			_, archive, err := s.readUpload(u.UploadID)
			if err != nil {
				t.Fatal(err)
			}
			txn := filepath.Join(s.root, ".staging", "install-"+randomID())
			nextDir := filepath.Join(txn, "new")
			if err := os.MkdirAll(nextDir, 0700); err != nil {
				t.Fatal(err)
			}
			m, _, _, err := s.extract(context.Background(), archive, nextDir)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := treeDigest(nextDir, true)
			if err != nil {
				t.Fatal(err)
			}
			prior := *s.records[old.ID]
			next := prior
			next.Manifest = m
			next.Revision = randomID()
			next.ContentSHA256 = digest
			next.ArchiveSHA256 = u.SHA256
			originalDesired := Desired{SchemaVersion: 1, State: "running", Revision: randomID()}
			j := installJournal{SchemaVersion: 1, AppID: old.ID, Old: &prior, Next: next, Phase: "prepared", OldDesired: &originalDesired}
			if err := writeJSON(filepath.Join(txn, "journal.json"), j); err != nil {
				t.Fatal(err)
			}
			code := filepath.Join(s.root, old.ID, "app")
			if stage >= 1 {
				if err := renamePackage(code, filepath.Join(txn, "old")); err != nil {
					t.Fatal(err)
				}
			}
			if stage >= 2 {
				if err := renamePackage(nextDir, code); err != nil {
					t.Fatal(err)
				}
			}
			if stage >= 3 {
				if err := writeJSON(filepath.Join(s.root, old.ID, "install.json"), next); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			reopened, err := Open(options)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			entry, _ := reopened.Get(old.ID)
			if entry.Version != "1.0.0" || entry.Revision != old.Revision || entry.DesiredState != "running" || entry.StateRevision != originalDesired.Revision {
				t.Fatalf("not rolled back %+v", entry)
			}
			data, _ := os.ReadFile(filepath.Join(code, "frontend", "index.html"))
			if !bytes.Contains(data, []byte("1.0.0")) {
				t.Fatal("old code was not restored")
			}
		})
	}
}
func TestUninstallKeepsDataUnlessExplicitlyPurgedAndReinstallGetsNewIdentity(t *testing.T) {
	s, _ := fixtureService(t)
	entry := installPackage(t, s, "1.0.0", "")
	identity := s.records[entry.ID].InstallationID
	dir := filepath.Join(s.dataRoot, entry.ID)
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "data"), []byte("keep"), 0600)
	if err := s.Uninstall(context.Background(), entry.ID, entry.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); err != nil {
		t.Fatal("data was deleted")
	}
	entry = installPackage(t, s, "1.0.0", "")
	if s.records[entry.ID].InstallationID == identity {
		t.Fatal("reinstall reused grant/receipt namespace")
	}
	if err := s.Uninstall(context.Background(), entry.ID, entry.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("explicit purge did not remove data")
	}
}
func TestUnknownRecordsAndDamagedCodeFailClosed(t *testing.T) {
	for _, damage := range []string{"schema", "code"} {
		t.Run(damage, func(t *testing.T) {
			s, options := fixtureService(t)
			entry := installPackage(t, s, "1.0.0", "")
			s.Close()
			dir := filepath.Join(s.root, entry.ID)
			if damage == "schema" {
				var r Record
				readJSON(filepath.Join(dir, "install.json"), &r)
				r.SchemaVersion = 99
				writeJSON(filepath.Join(dir, "install.json"), r)
			} else {
				name := filepath.Join(dir, "app", "frontend", "index.html")
				os.Chmod(name, 0600)
				os.WriteFile(name, []byte("changed"), 0600)
			}
			requireUnavailable(t, options, entry.ID, "webapp_installation_invalid")
		})
	}
}

// requireUnavailable checks that a damaged installation neither stops the
// host from opening nor gets loaded, overwritten or deleted.
func requireUnavailable(t *testing.T, options Options, id, code string) {
	t.Helper()
	recovered, err := Open(options)
	if err != nil {
		t.Fatalf("one damaged installation stopped the host: %v", err)
	}
	defer recovered.Close()
	if _, err := recovered.Get(id); err == nil {
		t.Fatal("damaged installation accepted")
	}
	issues := recovered.Issues()
	if len(issues) != 1 || issues[0] != code {
		t.Fatalf("issues = %v, want %s", issues, code)
	}
	if _, err := os.Stat(filepath.Join(recovered.root, id)); err != nil {
		t.Fatal("failure deleted evidence")
	}
	u := uploadPackage(t, recovered, "1.0.0")
	_, err = recovered.Install(context.Background(), InstallRequest{UploadID: u.UploadID, SHA256: u.SHA256})
	requireCode(t, err, "webapp_recovery_required")
}

func TestReopenKeepsSDKStateAndHealthyInstallations(t *testing.T) {
	s, options := fixtureService(t)
	entry := installPackage(t, s, "1.0.0", "")
	if err := os.MkdirAll(filepath.Join(options.Root, SDKStateDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.Root, "unexpected"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened, err := Open(options)
	if err != nil {
		t.Fatalf("SDK state or a stray entry stopped the host: %v", err)
	}
	defer reopened.Close()
	if _, err := reopened.Get(entry.ID); err != nil || len(reopened.Issues()) != 0 {
		t.Fatalf("healthy installation not restored: %v %v", err, reopened.Issues())
	}
}

func TestUnrecoverableJournalConfinedToItsApplication(t *testing.T) {
	s, options := fixtureService(t)
	entry := installPackage(t, s, "1.0.0", "")
	next := *s.records[entry.ID]
	s.Close()
	txn := filepath.Join(options.Root, ".staging", "install-"+randomID())
	if err := os.Mkdir(txn, 0700); err != nil {
		t.Fatal(err)
	}
	// A committed journal whose record does not match cannot be finished.
	next.Revision = randomID()
	j := installJournal{SchemaVersion: 1, AppID: entry.ID, Next: next, Phase: "committed"}
	if err := writeJSON(filepath.Join(txn, "journal.json"), j); err != nil {
		t.Fatal(err)
	}
	requireUnavailable(t, options, entry.ID, "webapp_recovery_required")
	if _, err := os.Stat(filepath.Join(txn, "journal.json")); err != nil {
		t.Fatal("unrecovered journal was removed")
	}
}

func TestSlowUploadDoesNotHoldServiceLock(t *testing.T) {
	s, _ := fixtureService(t)
	entry := installPackage(t, s, "1.0.0", "")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := s.Upload(context.Background(), reader)
		done <- err
	}()
	if _, err := writer.Write([]byte("PK")); err != nil {
		t.Fatal(err)
	}
	listed := make(chan struct{})
	go func() {
		_, _ = s.Get(entry.ID)
		close(listed)
	}()
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("service lock held while the upload body was still arriving")
	}
	writer.Close()
	<-done
}
func TestComposeSubsetRejectsOtherTargetsLaunchersAndEnvironmentOverrides(t *testing.T) {
	for _, mutation := range []map[string]any{{"target": "darwin-arm64"}, {"backend": map[string]any{"command": map[string]any{"type": "executable", "entry": "backend/run"}, "health": map[string]any{"type": "tcp"}}}, {"backend": map[string]any{"command": map[string]any{"type": "electron-node", "entry": "backend/server.mjs"}, "health": map[string]any{"type": "tcp"}, "env": map[string]any{"NODE_OPTIONS": "--inspect"}}}} {
		var m map[string]any
		raw := packageManifest(t)
		json.Unmarshal(raw, &m)
		for k, v := range mutation {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		parsed, err := ParseManifest(b)
		if err == nil && validateCompose(parsed) == nil {
			t.Fatal("unsupported Compose package accepted")
		}
	}
}
func packageManifest(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"test-app","label":"Test","version":"1.0.0","target":"linux-x64","frontend":{"root":"frontend","index":"index.html"}}`)
}
