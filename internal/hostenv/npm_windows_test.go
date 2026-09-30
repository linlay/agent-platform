package hostenv

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// The copied test binary acts as node.exe so npm's Windows entry-point adapter
// is exercised without installing Node or calling a user's real npm.
func TestMain(m *testing.M) {
	if prefix := os.Getenv("AP_HOSTENV_TEST_NPM"); prefix != "" {
		console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if console != 0 || len(os.Args) != 4 || filepath.Base(os.Args[1]) != "npm-cli.js" || os.Args[2] != "prefix" || os.Args[3] != "-g" {
			os.Exit(9)
		}
		fmt.Fprintln(os.Stdout, prefix)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsNPMDiscoveryDoesNotAllocateConsole(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "node with spaces")
	entry := filepath.Join(bin, "node_modules", "npm", "bin", "npm-cli.js")
	if err := os.MkdirAll(filepath.Dir(entry), 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(bin, "npm.cmd"), entry} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	dest, err := os.Create(filepath.Join(bin, "node.exe"))
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy helper: %v, %v", copyErr, closeErr)
	}
	prefix := filepath.Join(t.TempDir(), "npm global")
	env := Set(Set(os.Environ(), "PATH", bin), "AP_HOSTENV_TEST_NPM", prefix)
	if got := npmBin(env); got != prefix {
		t.Fatalf("hidden npm prefix probe failed: got %q, want %q", got, prefix)
	}
}
