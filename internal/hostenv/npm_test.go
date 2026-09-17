package hostenv

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNPMDiscoveryRefreshAndIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake npm")
	}
	tools := t.TempDir()
	prefix := filepath.Join(t.TempDir(), "global with spaces")
	env := []string{"PATH=" + tools, "HOME=" + t.TempDir()}
	Refresh()
	if got := WithNPM(env); Value(got, "PATH") != tools {
		t.Fatal(got)
	}
	script := "#!/bin/sh\n[ \"$1 $2\" = 'prefix -g' ] || exit 8\nprintf '%s\\n' '" + prefix + "'\n"
	if err := os.WriteFile(filepath.Join(tools, "npm"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if got := WithNPM(env); Value(got, "PATH") != tools {
		t.Fatal("negative cache unexpectedly changed")
	}
	Refresh()
	got := WithNPM(env)
	want := tools + ":" + filepath.Join(prefix, "bin")
	if Value(got, "PATH") != want || Value(env, "PATH") != tools {
		t.Fatalf("PATH=%s", Value(got, "PATH"))
	}
	// The refreshed positive result is cached without invoking npm again.
	os.Remove(filepath.Join(tools, "npm"))
	if Value(WithNPM(env), "PATH") != want {
		t.Fatal("cache not used")
	}
	Refresh()
	if Value(WithNPM(env), "PATH") != tools {
		t.Fatal("stale prefix")
	}
}
func TestReviewedPATHSurvivesShellProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash required")
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "demo"), []byte("#!/bin/sh\nprintf expected"), 0755); err != nil {
		t.Fatal(err)
	}
	env := Set(os.Environ(), "PATH", dir+":"+os.Getenv("PATH"))
	env = Set(env, "DEMO_CONFIG_DIR", "/expected/config")
	script := "export PATH=/missing; export DEMO_CONFIG_DIR=/wrong\n" + BindShellEnvironment(bash, "demo; printf ':%s' \"$DEMO_CONFIG_DIR\"", env, map[string]string{"DEMO_CONFIG_DIR": "/expected/config"})
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "expected:/expected/config" {
		t.Fatalf("%s %v", out, err)
	}
}
func TestNPMGlobalPlatformLayout(t *testing.T) {
	for _, tt := range []struct{ goos, want string }{{"darwin", "/prefix/bin"}, {"linux", "/prefix/bin"}, {"windows", "/prefix"}} {
		if got := npmGlobalBin("/prefix", tt.goos); filepath.ToSlash(got) != tt.want {
			t.Fatal(tt, got)
		}
	}
	if strings.Contains(npmGlobalBin("/prefix", "windows"), "node_modules") {
		t.Fatal("module path is not executable PATH")
	}
}

// Seed the discovery cache so this exercises the same merge on macOS and Windows
// without needing a real npm installation. Real command resolution verifies priority.
func TestNPMDiscoveryPreservesRuntimePriority(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			desktop, global := t.TempDir(), t.TempDir()
			node := "node"
			if runtime.GOOS == "windows" {
				node = "node.exe"
			}
			for _, dir := range []string{desktop, global} {
				if err := os.WriteFile(filepath.Join(dir, node), []byte("fixture"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			command := "global-cli"
			if runtime.GOOS == "windows" {
				command += ".exe"
			}
			if err := os.WriteFile(filepath.Join(global, command), []byte("fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			original := desktop
			if present {
				original += string(os.PathListSeparator) + global
			}
			env := []string{"PATH=" + original}
			key := sha256.Sum256([]byte(strings.Join(env, "\x00")))
			npmCache.Lock()
			npmCache.values[key] = global
			npmCache.Unlock()
			t.Cleanup(Refresh)
			got := WithNPM(env)
			want := desktop + string(os.PathListSeparator) + global
			if Value(got, "PATH") != want || Value(env, "PATH") != original {
				t.Fatal(got, env)
			}
			resolved, err := LookPath("node", got)
			if err != nil || resolved != filepath.Join(desktop, node) {
				t.Fatal(resolved, err)
			}
			if _, err := LookPath("global-cli", got); err != nil {
				t.Fatal(err)
			}
			if Value(WithNPM(got), "PATH") != want {
				t.Fatal("repeated enrichment changed priority")
			}
		})
	}
}
