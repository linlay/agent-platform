package connectorauth

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
)

func TestDisconnectCleansPrivateLoginEnvironmentAfterFailedSignOut(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	cli := simpleCLI()
	cli["auth"] = osCommands("demo login")
	cli["status"] = osCommands("demo status")
	cli["unAuth"] = osCommands("demo logout")
	cli["statusMatchJson"] = map[string]any{"authenticated": true}
	writeCLIPackage(t, sources.ExternalRoot, "demo", cli)
	script := filepath.Join(t.TempDir(), "demo.js")
	body := `if(process.argv[2]==='--version'){console.log('1.2.0')}else if(process.argv[2]==='logout'){process.exit(74)}else{console.log(JSON.stringify({authenticated:true}))}`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	m := New(t.Context(), sources, nil)
	if _, err := m.Prepare(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	private, err := CLIPrivateEnvironmentDirs(pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range private {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "login-material"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stateDir, err := pkg.ConnectorStateDir()
	if err != nil {
		t.Fatal(err)
	}
	preserved := []string{
		filepath.Join(stateDir, "installations", "shared-program"),
		filepath.Join(stateDir, "invocations", "completed-receipt"),
		filepath.Join(sources.PersistentRoot(), "another-connector", "config", "login"),
		filepath.Join(t.TempDir(), "real-user-home", "settings"),
	}
	for _, file := range preserved {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", filepath.Dir(preserved[len(preserved)-1]))
	preparation, err := os.ReadFile(filepath.Join(stateDir, "preparation.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Disconnect(t.Context(), "demo")
	if err != nil || result.Configured || len(result.Warnings) == 0 || result.RemoteRevocation != "unsupported" {
		t.Fatal("failed upstream sign-out prevented local cleanup", result, err)
	}
	for _, dir := range private {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("private login material retained", err)
		}
	}
	for _, file := range preserved {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "keep" {
			t.Fatal("disconnect removed unrelated state", err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(stateDir, "preparation.json")); err != nil || string(data) != string(preparation) {
		t.Fatal("disconnect removed prepared installation state", err)
	}
	if state, err := readAuthState(sources.PersistentRoot(), "demo"); err != nil || state.Generation == "" {
		t.Fatal("disconnect lost durable authorization fence", err)
	}
}
