//go:build !windows

package connectorauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func shellCLIPackage(t *testing.T, cli map[string]any) (*Manager, connector.Package, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "connectors")
	cli["env"] = map[string]any{"CFG": "${CONNECTOR_CONFIG_DIR}"}
	pkg := writeCLIPackage(t, root, "demo", cli)
	m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := m.Prepare(ctx, pkg.ID); err != nil {
		t.Fatal(err)
	}
	dir, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	return m, pkg, filepath.Join(dir, "config")
}

func TestCLILoginWithoutWaitPollsStatusAfterLink(t *testing.T) {
	m, _, config := shellCLIPackage(t, map[string]any{
		// The command never exits by itself; the link ends the step.
		"auth":   osCommands(`echo "open https://login.example.test/authorize?x=1"; sleep 60`),
		"status": osCommands(`test -f "$CFG/login"`), "unAuth": osCommands(`rm -f "$CFG/login"`),
		"authUrlDomain": "login.example.test",
	})
	if _, err := m.Start("demo"); err != nil {
		t.Fatal(err)
	}
	if s := waitStatus(t, m, "demo", "pending"); s.URL != "https://login.example.test/authorize?x=1" || s.UserCode != "" {
		t.Fatalf("pending session: %+v", s)
	}
	if err := os.WriteFile(filepath.Join(config, "login"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "demo", "authorized")
	if err := m.Logout(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config, "login")); !os.IsNotExist(err) {
		t.Fatal("unAuth did not run through the shell")
	}
}

func TestCLIDeviceFlowPublishesCodeAndWithholdsLinkWhenCLIOpensBrowser(t *testing.T) {
	m, _, config := shellCLIPackage(t, map[string]any{
		"auth":   osCommands(`echo "code: AB-12 at https://login.example.test/device"; while [ ! -f "$CFG/approve" ]; do sleep 0.05; done; touch "$CFG/login"`),
		"status": osCommands(`test -f "$CFG/login" && echo '{"authenticated":true}'`), "unAuth": osCommands(""),
		"statusMatchJson":     map[string]any{"authenticated": true},
		"authSuppressBrowser": true,
		"authDeviceFlow":      map[string]any{"uriPattern": `(https://\S+/device)`, "codePattern": `code: (\S+)`, "defaultExpiresInSeconds": 120.0},
	})
	if _, err := m.Start("demo"); err != nil {
		t.Fatal(err)
	}
	s := waitStatus(t, m, "demo", "pending")
	if s.URL != "" || s.UserCode != "AB-12" || s.UserCodeExpiresAt == nil || !strings.Contains(s.Message, "browser opened by the CLI") {
		t.Fatalf("device session: %+v", s)
	}
	if err := os.WriteFile(filepath.Join(config, "approve"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := waitStatus(t, m, "demo", "authorized"); s.UserCode != "" {
		t.Fatal("device code outlived the login", s)
	}
}

func TestCLIInitRetriesNextRegistryOnlyOnNetworkErrors(t *testing.T) {
	versionCheck := map[string]any{"command": osCommands("sh -c 'echo 1.0.0'"), "minVersion": "1.0.0"}
	for _, tc := range []struct {
		failure, want string
		ok            bool
	}{
		{"npm ERR! code ECONNRESET", "https://a.example.test\nhttps://b.example.test\n", true},
		{"npm ERR! 404 Not Found", "https://a.example.test\n", false},
	} {
		init := osCommands(`echo "$npm_config_registry" >> "$CFG/registries"; [ "$npm_config_registry" = https://b.example.test ] || { echo "` + tc.failure + `"; exit 1; }`)
		root := filepath.Join(t.TempDir(), "connectors")
		cli := map[string]any{"init": init, "versionCheck": versionCheck, "npmRegistries": []any{"https://a.example.test", "https://b.example.test"}, "env": map[string]any{"CFG": "${CONNECTOR_CONFIG_DIR}"}}
		pkg := writeCLIPackage(t, root, "demo", cli)
		m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
		_, err := m.Prepare(t.Context(), pkg.ID)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: prepare %v", tc.failure, err)
		}
		dir, _ := pkg.ConnectorStateDir()
		data, _ := os.ReadFile(filepath.Join(dir, "config", "registries"))
		if string(data) != tc.want {
			t.Fatalf("%s: registries tried %q", tc.failure, data)
		}
		if installation, err := connector.ReadInstallation(pkg.InstallRoot, pkg.ID); tc.ok && (err != nil || installation.Executable != "") {
			t.Fatal("a launcher in versionCheck became the pinned executable", installation, err)
		}
	}
}
