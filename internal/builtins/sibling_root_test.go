package builtins

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDefaultSourceRootFromLinkedWorktree(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "projects/platform")
	if err := os.MkdirAll(main, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
	}
	git("init", main)
	git("-C", main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	worktree := filepath.Join(root, "worktrees/task")
	git("-C", main, "worktree", "add", "--detach", worktree)
	sibling := filepath.Join(root, "projects/agent-platform-builtins")
	if e := os.MkdirAll(sibling, 0755); e != nil {
		t.Fatal(e)
	}
	if got := DefaultSourceRoot(worktree, "../agent-platform-builtins"); got != sibling {
		t.Fatalf("got %s want %s", got, sibling)
	}
	local := filepath.Join(root, "worktrees/agent-platform-builtins")
	if e := os.MkdirAll(local, 0755); e != nil {
		t.Fatal(e)
	}
	if got := DefaultSourceRoot(worktree, "../agent-platform-builtins"); got != local {
		t.Fatalf("local sibling must take precedence: %s", got)
	}
	t.Setenv("BUILTINS_ROOT", sibling)
	got, e := ResolveRoot(worktree, "", Lock{DefaultRoot: "../agent-platform-builtins"})
	if e != nil || got != sibling {
		t.Fatalf("explicit environment lost: %s %v", got, e)
	}
}
func TestRequireKBXComponent(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		m := Manifest{Platform: ManifestPlatform{OS: goos, Arch: "arm64"}, Components: []ManifestComponent{{Name: "kbase-lance-engine", Path: "bin/kbase-lance-engine"}}}
		if RequireKBXComponent(m) == nil {
			t.Fatal("old cache accepted")
		}
		path := "bin/kbx"
		if goos == "windows" {
			path += ".exe"
		}
		m.Components = append(m.Components, ManifestComponent{Name: "kbx", Path: path})
		if e := RequireKBXComponent(m); e != nil {
			t.Fatal(e)
		}
	}
}

func TestRequireMemxComponent(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		manifest := Manifest{Platform: ManifestPlatform{OS: goos, Arch: "arm64"}, Components: []ManifestComponent{{Name: "kbx", Path: "bin/kbx"}}}
		if RequireMemxComponent(manifest) == nil {
			t.Fatal("cache without memx accepted")
		}
		expected := "bin/memx"
		if goos == "windows" {
			expected += ".exe"
		}
		manifest.Components = append(manifest.Components, ManifestComponent{Name: "memx", Path: "wrong/memx"})
		if RequireMemxComponent(manifest) == nil {
			t.Fatal("wrong memx path accepted")
		}
		manifest.Components[1].Path = expected
		if err := RequireMemxComponent(manifest); err != nil {
			t.Fatal(err)
		}
	}
}
