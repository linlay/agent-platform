//go:build !windows

package connectorauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

// fakeNPMInit links a "demo" command into npm's global prefix like npm -g.
func fakeNPMInit(version string, fail bool) map[string]any {
	line := `mkdir -p "$npm_config_prefix/lib/demo" "$npm_config_prefix/bin" && printf '#!/bin/sh\necho ` + version + `\n' > "$npm_config_prefix/lib/demo/cli" && chmod +x "$npm_config_prefix/lib/demo/cli" && ln -sf ../lib/demo/cli "$npm_config_prefix/bin/demo" && echo "$npm_config_cache" > "$npm_config_prefix/cache"`
	if fail {
		line = "echo 'npm ERR! 404 Not Found'; exit 1"
	}
	return map[string]any{
		"init":         osCommands(line),
		"versionCheck": map[string]any{"command": osCommands("demo --version"), "minVersion": "1.0.0"},
	}
}

func writeVersionedCLIPackage(t *testing.T, root, version string, cli map[string]any) connector.Package {
	t.Helper()
	pkg := writeCLIPackage(t, root, "demo", cli)
	manifest := pkg.Manifest
	manifest.Version = version
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pkg.Dir, "connector.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := (connector.Sources{ExternalRoot: root}).Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestPreparationInstallsIntoPrivatePrefixAndKeepsLastGoodInstallation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connectors-center")
	sources := connector.Sources{ExternalRoot: root}
	m := New(t.Context(), sources, nil)
	ctx := context.Background()

	first := writeVersionedCLIPackage(t, root, "1.0.0", fakeNPMInit("1.0.0", false))
	if _, err := m.Prepare(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	active, err := connector.ReadInstallation(sources.InstallRoot(), "demo")
	if err != nil || !strings.HasPrefix(active.Prefix, filepath.Join(sources.InstallRoot(), "demo", "1.0.0")+string(filepath.Separator)) {
		t.Fatal("init did not install into the private prefix", active, err)
	}
	if active.Executable != filepath.Join(active.Bin(), "demo") || active.ExecutableSHA256 == "" {
		t.Fatal("installed executable not pinned", active)
	}
	if cache, _ := os.ReadFile(filepath.Join(active.Prefix, "cache")); strings.TrimSpace(string(cache)) != filepath.Join(sources.InstallRoot(), ".npm-cache") {
		t.Fatalf("npm cache is not shared under connector-installs: %q", cache)
	}
	if _, err := os.Stat(filepath.Join(sources.InstallRoot(), "demo", "preparation.json")); err != nil {
		t.Fatal("preparation record not stored with programs", err)
	}
	if dir, _ := first.ConnectorStateDir(); fileExists(filepath.Join(dir, "preparation.json")) {
		t.Fatal("preparation record stored in .state")
	}

	// A failed update keeps the previous installation in use.
	writeVersionedCLIPackage(t, root, "1.1.0", fakeNPMInit("1.1.0", true))
	if _, err := m.Prepare(ctx, "demo"); err == nil {
		t.Fatal("failing init reported ready")
	}
	status, err := m.PreparationStatus("demo")
	if err != nil || status.Status != "failed" || status.ActiveVersion != "1.0.0" || !strings.Contains(status.Diagnostic, "404") {
		t.Fatal("failed update hid the active installation", status, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(sources.InstallRoot(), "demo", "1.1.0")); len(entries) != 0 {
		t.Fatal("failed attempt left its installation directory", entries)
	}
	pkg, _ := sources.Load("demo")
	if err := m.requirePrepared(pkg); err != nil {
		t.Fatal("previous installation stopped serving", err)
	}
	if c, err := m.Connection(ctx, "demo"); err != nil || c.Readiness == "unavailable" || c.Readiness == "preparing" {
		t.Fatal("failed update made the connector unavailable", c.Readiness, err)
	}

	// A retry of the same package installs beside the active installation.
	writeVersionedCLIPackage(t, root, "1.2.0", fakeNPMInit("1.2.0", false))
	if _, err := m.Prepare(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	second, _ := connector.ReadInstallation(sources.InstallRoot(), "demo")
	// A Run still using the second installation retains it across updates.
	release, err := connector.RetainInstallation(second.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Prepare(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	third, _ := connector.ReadInstallation(sources.InstallRoot(), "demo")
	if third.Prefix == second.Prefix {
		t.Fatal("retry installed into the active installation")
	}
	for prefix, want := range map[string]bool{active.Prefix: false, second.Prefix: true, third.Prefix: true} {
		if fileExists(prefix) != want {
			t.Fatalf("installation %s kept=%v, want %v", prefix, !want, want)
		}
	}
	release()
	sources.CollectInstallations()
	if fileExists(second.Prefix) || !fileExists(third.Prefix) {
		t.Fatal("collection kept a released installation or removed the active one")
	}

	// Deleting the package removes its programs.
	if err := connector.DeletePackage(ctx, sources, "demo", nil, nil); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(sources.InstallRoot(), "demo")) {
		t.Fatal("installations survived package deletion")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestUnpinnedNPMInitAndDiagnosticExcerpt(t *testing.T) {
	for line, want := range map[string]bool{
		"npm install -g @larksuite/cli":                         true,
		"npm install -g @larksuite/cli@latest":                  true,
		"npm install -g @larksuite/cli@1.0.94":                  false,
		"npm i -g --registry https://r.example a@1.2.3 b@2.0.0": false,
		"node -v && npm install -g a@1.2.3 && npm i -g b":       true,
		"curl -fsSL https://example.test/install.sh | sh":       false,
	} {
		if got := unpinnedNPMInit(map[string]any{"init": osCommands(line)}); got != want {
			t.Fatalf("%q: got %v", line, got)
		}
	}
	excerpt := diagnosticExcerpt("GET https://user:pass@registry.example/a\n//registry.example/:_authToken=npm_secret\nAuthorization: Bearer abc.def\n\x1b[31mnpm ERR! 404\x1b[0m")
	for _, secret := range []string{"user:pass", "npm_secret", "abc.def", "\x1b"} {
		if strings.Contains(excerpt, secret) {
			t.Fatalf("diagnostic kept %q: %s", secret, excerpt)
		}
	}
	if !strings.Contains(excerpt, "npm ERR! 404") {
		t.Fatal("diagnostic lost the failure", excerpt)
	}
}

func TestUpdateInProgressKeepsActiveInstallationAndReloadsAfterSuccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connectors-center")
	sources := connector.Sources{ExternalRoot: root}
	reloads := 0
	m := New(t.Context(), sources, nil).WithPreparationReload(func(context.Context) error { reloads++; return nil })
	writeVersionedCLIPackage(t, root, "1.0.0", fakeNPMInit("1.0.0", false))
	if _, err := m.Prepare(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if reloads != 1 {
		t.Fatalf("successful preparation reloaded the catalog %d times", reloads)
	}
	// The update's init blocks until released.
	cli := fakeNPMInit("1.1.0", false)
	cli["init"] = osCommands(`while [ ! -f "` + filepath.Join(t.TempDir(), "never") + `" ]; do sleep 0.05; done`)
	writeVersionedCLIPackage(t, root, "1.1.0", cli)
	if _, err := m.StartPreparation("demo"); err != nil {
		t.Fatal(err)
	}
	defer m.CancelPreparation("demo")
	status, err := m.PreparationStatus("demo")
	if err != nil || status.Status != "preparing" || status.ActiveVersion != "1.0.0" {
		t.Fatal("preparing status hid the active installation", status, err)
	}
	if c, err := m.Connection(context.Background(), "demo"); err != nil || c.Readiness == "preparing" || c.Readiness == "unavailable" {
		t.Fatal("update in progress blocked the active installation", c.Readiness, err)
	}
	// A catalog reload's collection never removes the attempt in progress.
	attempts, _ := os.ReadDir(filepath.Join(sources.InstallRoot(), "demo", "1.1.0"))
	if len(attempts) != 1 {
		t.Fatal("missing attempt directory", attempts)
	}
	sources.CollectInstallations()
	if !fileExists(filepath.Join(sources.InstallRoot(), "demo", "1.1.0", attempts[0].Name())) {
		t.Fatal("collection removed an installation in progress")
	}
}
