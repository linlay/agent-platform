package skillsexec

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"agent-platform/internal/scriptstate"
)

func fixture(t *testing.T) (string, string, scriptstate.Owner) {
	t.Helper()
	root := t.TempDir()
	scripts := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scripts, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(scripts, "task.sh")
	if err := os.WriteFile(file, []byte("echo original\n"), 0700); err != nil {
		t.Fatal(err)
	}
	root, _ = strictPath(root)
	file, _ = strictPath(file)
	return root, file, scriptstate.Owner{Agent: "a", Run: "r", Environment: "host:"}
}
func TestSnapshotScopeAndRevocation(t *testing.T) {
	root, file, owner := fixture(t)
	s := New(owner, []Root{{Host: root, Guest: "/skills/selected"}})
	if !s.Matches(owner, file, "", false) {
		t.Fatal("missing grant")
	}
	for _, foreign := range []scriptstate.Owner{{Agent: "b", Run: "r", Environment: "host:"}, {Agent: "a", Run: "other", Environment: "host:"}, {Agent: "a", Run: "r", Environment: "sandbox:x"}} {
		if s.Matches(foreign, file, "", false) {
			t.Fatal("owner escaped")
		}
	}
	for _, p := range []string{filepath.Join(root, "root.sh"), filepath.Join(root, "scripts", "new.sh")} {
		if err := os.WriteFile(p, []byte("echo original\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if s.Matches(owner, p, "", false) {
			t.Fatal("new/unscanned file gained grant")
		}
	}
	if p, ok := s.HostPath("/skills/selected/scripts/task.sh"); !ok || p != file {
		t.Fatalf("mapping %s %t", p, ok)
	}
	for _, p := range []string{"/skills/sibling/scripts/task.sh", "/skills/selected-other/task.sh", "/skills/selected/../sibling/task.sh"} {
		if _, ok := s.HostPath(p); ok {
			t.Fatal("mapping escaped: ", p)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if !s.Matches(owner, file, "", false) {
					t.Error("concurrent grant lost")
				}
			}
		}()
	}
	wg.Wait()
	if err := os.WriteFile(file, []byte("echo changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if s.Matches(owner, file, "", false) {
		t.Fatal("changed file allowed")
	}
	if err := os.WriteFile(file, []byte("echo original\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if s.Matches(owner, file, "", false) {
		t.Fatal("revoked grant restored")
	}
}
func TestContainerDigestAndMissingFile(t *testing.T) {
	root, file, owner := fixture(t)
	s := New(owner, []Root{{Host: root}})
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("echo original\n")))
	if !s.Matches(owner, file, hash, true) {
		t.Fatal("container matching bytes denied")
	}
	if s.Matches(owner, file, "different", true) || s.Matches(owner, file, hash, true) {
		t.Fatal("container mismatch not revoked")
	}
	s = New(owner, []Root{{Host: root}})
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if s.Matches(owner, file, "", false) {
		t.Fatal("missing file allowed")
	}
	if err := os.WriteFile(file, []byte("echo original\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if s.Matches(owner, file, "", false) {
		t.Fatal("missing file restored grant")
	}
}
func TestSymlinkEscapeAndRootReplacement(t *testing.T) {
	root, file, owner := fixture(t)
	outside := t.TempDir()
	foreign := filepath.Join(outside, "foreign.sh")
	if err := os.WriteFile(foreign, []byte("echo original\n"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "scripts", "escape.sh")
	if err := os.Symlink(foreign, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	s := New(owner, []Root{{Host: root}})
	if s.Matches(owner, link, "", false) {
		t.Fatal("escape granted")
	}
	saved := root + "-old"
	if err := os.Rename(root, saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(root); _ = os.Rename(saved, root) })
	if err := os.Symlink(saved, root); err != nil {
		t.Fatal(err)
	}
	if s.Matches(owner, file, "", false) {
		t.Fatal("root replacement granted")
	}
}
