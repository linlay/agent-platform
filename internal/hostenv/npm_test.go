package hostenv

import (
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
	want := filepath.Join(prefix, "bin") + ":" + tools
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
