package connectorauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/connector"
)

func TestFailedCLIUpdateKeepsVerifiedActivePackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX payload integration")
	}
	root := t.TempDir()
	cli := simpleCLI()
	cli["auth"] = osCommands("demo login")
	cli["unAuth"] = osCommands("demo logout")
	cli["status"] = osCommands("demo status")
	cli["statusMatchJson"] = map[string]any{"authenticated": true}
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	oldPayload := `#!/bin/sh
case "$1" in
  --version) printf '1.2.0\n';;
  login) printf session > "$XDG_CONFIG_HOME/login";;
  logout) rm -f "$XDG_CONFIG_HOME/login";;
  *) if test -f "$XDG_CONFIG_HOME/login"; then printf '{"authenticated":true}\n'; else printf '{"authenticated":false}\n'; fi;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "bin", "demo"), []byte(oldPayload), 0755); err != nil {
		t.Fatal(err)
	}
	pkg := writeCLIPackage(t, root, "demo", cli)
	sources := connector.Sources{ExternalRoot: root}
	m := New(context.Background(), sources, nil)
	if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(pkg.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, pkg.ID, "authorized")
	active, err := ResolveActiveCLIPackage(sources, pkg)
	if err != nil || active.Version != "1.0.0" || active.Dir == pkg.Dir {
		t.Fatalf("initial prepared package was not activated immutably: %+v %v", active, err)
	}
	oldActiveDir := active.Dir
	// Publish a different source version, while retaining the same command name.
	// Its insufficient binary and failed init cannot damage the shared old copy.
	cli["init"] = osCommands("exit 7")
	writeCLIPackage(t, root, "demo", cli)
	manifest := pkg.Manifest
	manifest.Version = "2.0.0"
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pkg.Dir, "connector.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, "bin", "demo"), []byte("#!/bin/sh\nprintf 0.1.0\\n"), 0755); err != nil {
		t.Fatal(err)
	}
	updated, err := sources.Load(pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := m.Prepare(context.Background(), pkg.ID); err == nil || state.Status != "failed" {
		t.Fatalf("failed update was accepted: %+v %v", state, err)
	}
	active, err = ResolveActiveCLIPackage(sources, updated)
	if err != nil || active.Version != "1.0.0" || active.Dir != oldActiveDir {
		t.Fatalf("failed update discarded old active installation: %+v %v", active, err)
	}
	if ok, err := m.cliStatus(context.Background(), active); err != nil || !ok {
		t.Fatalf("old activated CLI stopped working after failed preparation: %v %v", ok, err)
	}
	if checked, err := m.Check(t.Context(), pkg.ID, ""); err != nil || checked.Status != "authorized" {
		t.Fatalf("failed source update blocked active CLI check: %+v %v", checked, err)
	}
	// Expire the old login locally, then exercise the public login path without
	// repairing or activating the failed source update.
	stateDir, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stateDir, "config", "login")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(pkg.ID); err != nil {
		t.Fatal("failed source update blocked active CLI login", err)
	}
	waitStatus(t, m, pkg.ID, "authorized")
	if checked, err := m.Check(t.Context(), pkg.ID, ""); err != nil || checked.Status != "authorized" {
		t.Fatalf("active CLI did not reauthenticate: %+v %v", checked, err)
	}
	if preparation, err := m.PreparationStatus(pkg.ID); err != nil || preparation.Status != "failed" {
		t.Fatalf("authentication changed failed source preparation: %+v %v", preparation, err)
	}
	if current, err := ResolveActiveCLIPackage(sources, updated); err != nil || current.Dir != oldActiveDir {
		t.Fatal("authentication changed active installation", err)
	}
	// No name-based fallback is allowed after the old immutable content changes.
	if err := os.WriteFile(filepath.Join(active.Dir, "bin", "demo"), []byte("#!/bin/sh\nprintf tampered\\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveActiveCLIPackage(sources, updated); err == nil {
		t.Fatal("tampered active package was selected")
	}
	if _, err := m.Check(t.Context(), pkg.ID, ""); err == nil {
		t.Fatal("check accepted tampered active package")
	}
	if _, err := m.Start(pkg.ID); err == nil {
		t.Fatal("login accepted tampered active package")
	}
}

func TestActiveCLIRecordRejectsForeignDirectories(t *testing.T) {
	root := t.TempDir()
	pkg := writeCLIPackage(t, root, "demo", simpleCLI())
	sources := connector.Sources{ExternalRoot: root}
	if resolved, err := ResolveActiveCLIPackage(sources, pkg); err != nil || resolved.Dir != pkg.Dir {
		t.Fatalf("missing active record guessed another installation: %+v %v", resolved, err)
	}
	path, err := activeInstallationPath(sources, pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest := "1111111111111111111111111111111111111111111111111111111111111111"
	prep := Preparation{ConnectorID: pkg.ID, Status: "ready", Fingerprint: digest}
	record := activeInstallationRecord{ConnectorID: pkg.ID, Dir: filepath.Join(t.TempDir(), digest), Digest: digest, PackageVersion: "1.0.0", InstallationDigest: digest,
		Preparation: preparationRecord{Preparation: prep, Executable: "/unknown/demo", ExecutableSHA256: digest}}
	if err := savePrivateJSON(path, record); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveActiveCLIPackage(sources, pkg); err == nil {
		t.Fatal("foreign active-package directory was trusted")
	}
}

func TestActiveCLIAuthenticationDoesNotCrossAuthModes(t *testing.T) {
	m, pkg, modePath := authenticationResultCLIFixture(t, true, false)
	if _, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"}); err != nil {
		t.Fatal(err)
	}
	// A new delegated source must not reinterpret an active token package as a
	// delegated CLI login, nor copy or clear the token account during fallback.
	delete(pkg.CLI, "env")
	pkg.CLI["auth"], pkg.CLI["unAuth"] = osCommands("demo login"), osCommands("demo logout")
	writeCLIPackage(t, m.sources.ExternalRoot, pkg.ID, pkg.CLI)
	if _, err := m.Start(pkg.ID); err == nil {
		t.Fatal("login crossed the active package authentication mode")
	}
	if _, err := m.Check(t.Context(), pkg.ID, ""); err == nil {
		t.Fatal("check crossed the active package authentication mode")
	}
	if _, err := os.Stat(modePath + ".login-called"); !os.IsNotExist(err) {
		t.Fatal("authentication executed against another mode")
	}
	values, ready, err := TokenValues(pkg)
	if err != nil || !ready || values["API_KEY"] != "good-secret" {
		t.Fatal("authentication fallback changed saved token credentials", err)
	}
}
