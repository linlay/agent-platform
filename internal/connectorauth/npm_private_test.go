package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNPMGlobalDeclarationUsesOnlyPrivatePrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake npm executes on Unix; Windows wrapper separately compiled")
	}
	for _, flag := range []string{"--global", "-g"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			system := t.TempDir()
			global := t.TempDir()
			marker := filepath.Join(global, "untouched")
			os.WriteFile(marker, []byte("original"), 0600)
			fake := `#!/bin/sh
prefix=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--prefix" ]; then shift; prefix="$1"; fi
  shift
done
[ -n "$prefix" ] || exit 41
[ "$prefix" = "$NPM_CONFIG_PREFIX" ] || exit 42
mkdir -p "$prefix/bin"
printf '#!/bin/sh\nprintf "1.4.0\\n"\n' > "$prefix/bin/demo"
chmod +x "$prefix/bin/demo"
printf '%s' "$prefix" > "$prefix/npm-called"
`
			os.WriteFile(filepath.Join(system, "npm"), []byte(fake), 0755)
			t.Setenv("PATH", system+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("NPM_CONFIG_PREFIX", global)
			cli := simpleCLI()
			cli["init"] = osCommands("npm install " + flag + " @example/office-cli@1.4.0")
			pkg := writeCLIPackage(t, root, "demo", cli)
			m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
			if _, err := m.Prepare(context.Background(), pkg.ID); err != nil {
				t.Fatal(err)
			}
			install, _ := pkg.InstallDir()
			called, err := os.ReadFile(filepath.Join(install, "npm-called"))
			if err != nil || string(called) != install {
				t.Fatal(string(called), err)
			}
			if data, _ := os.ReadFile(marker); string(data) != "original" {
				t.Fatal("user global prefix modified")
			}
			items, _ := os.ReadDir(global)
			if len(items) != 1 {
				t.Fatal("user global install touched")
			}
			if data, _ := os.ReadFile(filepath.Join(install, "setup", "npm-wrapper", "npm")); !strings.Contains(string(data), "--prefix") {
				t.Fatal("missing private shim")
			}
		})
	}
}

func TestNPMWindowsShimPinsPrefixWithoutChangingGlobalFlag(t *testing.T) {
	name, script, err := npmShimScript("windows", `C:\Program Files\nodejs\npm.cmd`, `C:\Private Tools\wecom\1.0.0`)
	if err != nil || name != "npm.cmd" || !strings.Contains(script, `%* --prefix "C:\Private Tools\wecom\1.0.0"`) {
		t.Fatal(name, script, err)
	}
	if _, _, err := npmShimScript("windows", `C:\npm.cmd`, `C:\bad%PATH%`); err == nil {
		t.Fatal("unsafe cmd path accepted")
	}
	if err := validatePrivateCLIInit(`npm.cmd install --global @example/office-cli@1.4.0`); err != nil {
		t.Fatal(err)
	}
}
