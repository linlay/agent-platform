package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCLIRequiresPinnedNPMButAllowsGlobalDeclaration(t *testing.T) {
	for _, line := range []string{
		"npm install -g good@1.2.3 bad", "npm.cmd install --global package",
		"npm install --prefix \"$CONNECTOR_INSTALL_DIR\" tool@latest",
		"npm install --prefix \"$CONNECTOR_INSTALL_DIR\" tool",
	} {
		if err := validatePrivateCLIInit(line); err == nil {
			t.Fatalf("accepted %s", line)
		}
	}
	for _, line := range []string{
		"npm install -g @example/office-cli@1.4.0", "npm.cmd install --global @example/office-cli@1.4.0",
		"npm install --prefix \"$CONNECTOR_INSTALL_DIR\" @scope/tool@1.2.3",
		"npm install --prefix \"%CONNECTOR_INSTALL_DIR%\" tool@1.2.3",
	} {
		if err := validatePrivateCLIInit(line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
}

func TestCLIWithoutInitAcceptsVerifiedHostProgram(t *testing.T) {
	root := t.TempDir()
	pkg := writeCLIPackage(t, root, "demo", simpleCLI())
	global := t.TempDir()
	os.WriteFile(filepath.Join(global, "demo"), []byte("#!/bin/sh\necho 1.2.0"), 0755)
	t.Setenv("PATH", global+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
		t.Fatal("declared host CLI was not accepted", err)
	}
}

func TestPreparationChecksVersionBeforeInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture; Windows execution compiled separately")
	}
	root := t.TempDir()
	cli := simpleCLI()
	cli["init"] = osCommands("printf ran > installer-ran")
	pkg := writeCLIPackage(t, root, "demo", cli)
	install, err := pkg.InstallDir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(install, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(install, "bin", "demo"), []byte("#!/bin/sh\nprintf '1.2.3\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err = m.Prepare(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(pkg.Dir, "installer-ran")); !os.IsNotExist(err) {
		t.Fatal("installer ran despite compatible version")
	}
}
