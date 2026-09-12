package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func osCommands(line string) map[string]any {
	return map[string]any{"darwin": line, "linux": line, "win32": line}
}
func writeCLIPackage(t *testing.T, root, id string, cli map[string]any) connector.Package {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := connector.Manifest{ID: id, Name: id, Version: "1.0.0", Type: "cli", AuthMode: connector.AuthDelegated}
	for name, value := range map[string]any{"connector.json": manifest, "cli.json": cli} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := (connector.Sources{ExternalRoot: root}).Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}
func testNodeCLI(t *testing.T, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	dir := t.TempDir()
	name := "demo"
	body := "#!/bin/sh\nexec '" + node + "' '" + script + "' \"$@\"\n"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		body = "@echo off\r\n\"" + node + "\" \"" + script + "\" %*\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func simpleCLI() map[string]any {
	return map[string]any{"versionCheck": map[string]any{"command": osCommands("demo --version"), "minVersion": "1.2.0"}}
}

func TestPreparationRawInitAndBundledPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script; Windows adapter tested separately")
	}
	root := t.TempDir()
	cli := simpleCLI()
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("TEST_CLI_BIN", bin)
	cli["init"] = osCommands("test -f connector.json && { printf '#!/bin/sh\\nprintf 1.2.0\\n' > \"$TEST_CLI_BIN/demo\"; chmod +x \"$TEST_CLI_BIN/demo\"; }\nprintf cwd-ok > initialized")
	pkg := writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	s, err := m.Prepare(context.Background(), pkg.ID)
	if err != nil || s.Status != "ready" {
		t.Fatalf("prepare: %+v %v", s, err)
	}
	if data, err := os.ReadFile(filepath.Join(pkg.Dir, "initialized")); err != nil || string(data) != "cwd-ok" {
		t.Fatalf("raw script cwd: %s %v", data, err)
	}
	// Empty bin is an explicit bundled package and cannot fall back to global demo.
	if err := os.Mkdir(filepath.Join(pkg.Dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(pkg.Dir, "initialized")); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Prepare(context.Background(), pkg.ID); err == nil {
		t.Fatal("empty bin used global executable")
	}
	if _, err = os.Stat(filepath.Join(pkg.Dir, "initialized")); !os.IsNotExist(err) {
		t.Fatal("bundled package executed init")
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, "bin", "demo"), []byte("#!/bin/sh\nprintf 1.2.0"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal(err)
	}
}
func TestPreparationCancellationConflictRecoveryAndRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process integration")
	}
	root := t.TempDir()
	cli := simpleCLI()
	cli["init"] = osCommands("printf begin; sleep 20; touch unexpected")
	pkg := writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	first, err := m.StartPreparation("demo")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.StartPreparation("demo")
	if err != nil || first.UpdatedAt != second.UpdatedAt {
		t.Fatal("duplicate job")
	}
	if err := connector.DeletePackage(context.Background(), m.sources, "demo", nil, nil); !errors.Is(err, connector.ErrBusy) {
		t.Fatalf("delete: %v", err)
	}
	file, err := connector.ReadFile(root, "demo", "cli.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connector.SaveDefinition(root, file, file.SHA256, nil, nil); !errors.Is(err, connector.ErrBusy) {
		t.Fatalf("edit: %v", err)
	}
	if err = m.CancelPreparation("demo"); err != nil {
		t.Fatal(err)
	}
	s, err := m.PreparationStatus("demo")
	if err != nil || s.Status != "canceled" {
		t.Fatalf("cancel: %+v %v", s, err)
	}
	if _, err = os.Stat(filepath.Join(pkg.Dir, "unexpected")); !os.IsNotExist(err) {
		t.Fatal("canceled child finished")
	}
	// Another Manager recovers persisted work only when the OS lock is free.
	if err = m.savePreparation(Preparation{ConnectorID: "demo", Status: "preparing"}); err != nil {
		t.Fatal(err)
	}
	s, err = New(context.Background(), m.sources, nil).PreparationStatus("demo")
	if err != nil || s.Status != "canceled" {
		t.Fatalf("recovery: %+v %v", s, err)
	}
	cli["init"] = osCommands("printf diagnostic >&2; exit 7")
	writeCLIPackage(t, root, "demo", cli)
	s, err = m.Prepare(context.Background(), "demo")
	if err == nil || s.ExitCode == nil || *s.ExitCode != 7 || !strings.Contains(s.Diagnostic, "diagnostic") {
		t.Fatalf("failure: %+v %v", s, err)
	}
}
func TestPreparationExternalCommandWithoutInit(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cli.js")
	os.WriteFile(script, []byte("console.log('1.2.0')"), 0600)
	testNodeCLI(t, script)
	root := t.TempDir()
	writeCLIPackage(t, root, "demo", simpleCLI())
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.Prepare(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("demo"); err == nil {
		t.Fatal("CLI without auth has interactive login")
	}
}

func TestGeneratedWorkBuddyCLIContracts(t *testing.T) {
	root := filepath.Join("..", "..", "build", "connectors", "packages")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("generated distribution not present")
	}
	for _, id := range []string{"wecom", "tmeet", "feishu"} {
		pkg, err := connector.Load(root, id)
		if err != nil {
			t.Fatal(err)
		}
		if pkg.BinDir != "" {
			t.Fatalf("%s still contains a launcher bin", id)
		}
		if err = ValidatePackage(pkg); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		platform, _ := pkg.CLI["platform"].(map[string]any)
		for _, key := range []string{"npmPackage", "npmVersion", "entry", "nativeEntry"} {
			if _, ok := platform[key]; ok {
				t.Fatalf("%s contains retired install setting %s", id, key)
			}
		}
		var upstream map[string]any
		if err = connector.ReadJSON(filepath.Join(pkg.Dir, "upstream", "cli.json"), &upstream); err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(upstream["init"])
		after, _ := json.Marshal(pkg.CLI["init"])
		if string(before) != string(after) {
			t.Fatalf("%s init was rewritten", id)
		}
	}
}
