package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRuntimeAgentRenameRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, platform  string
		code            syscall.Errno
		failures, calls int
	}{
		{"windows recovers", "windows", 32, 2, 3},
		{"windows access denied bounded", "windows", 5, 10, 6},
		{"windows lock violation", "windows", 33, 1, 2},
		{"windows other error", "windows", 2, 10, 1},
		{"macOS no retry", "darwin", 5, 10, 1},
		{"linux no retry", "linux", 5, 10, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var waited time.Duration
			err := renameRuntimeAgent("publish", source, target, tc.platform, func(a, b string) error {
				calls++
				if calls <= tc.failures {
					return &os.LinkError{Op: "rename", Old: a, New: b, Err: tc.code}
				}
				return os.Rename(a, b)
			}, func(d time.Duration) { waited += d })
			if calls != tc.calls {
				t.Fatalf("calls=%d want=%d", calls, tc.calls)
			}
			if tc.failures < tc.calls {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(target); err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, tc.code) {
					t.Fatalf("lost original error: %v", err)
				}
				for _, field := range []string{"stage=publish", "pid=", "attempts=", "elapsed_ms=", "os_error_code=", "source_state={exists=true", "target_state={exists=false", "source_parent_state=", "target_parent_state="} {
					if !strings.Contains(err.Error(), field) {
						t.Fatalf("missing %s: %v", field, err)
					}
				}
			}
			if calls == 6 && waited != 1550*time.Millisecond {
				t.Fatalf("waited=%v", waited)
			}
			if calls == 1 && waited != 0 {
				t.Fatalf("unexpected sleep=%v", waited)
			}
		})
	}
}

func TestRuntimeAgentRenamePreservesRecreatedDestination(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := renameRuntimeAgent("publish", source, target, "windows", func(a, b string) error {
		calls++
		return syscall.Errno(32)
	}, func(time.Duration) { writeRuntimeAssemblerFile(t, filepath.Join(target, "sentinel"), "other writer") })
	if calls != 1 || !errors.Is(err, os.ErrExist) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	assertRuntimeAssemblerContent(t, filepath.Join(target, "sentinel"), "other writer")
}

func TestRuntimeAgentPublicationRollback(t *testing.T) {
	for _, failRollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "retained backup"}[failRollback], func(t *testing.T) {
			root := t.TempDir()
			candidate, stable := filepath.Join(root, "candidate"), filepath.Join(root, "stable")
			writeRuntimeAssemblerFile(t, filepath.Join(candidate, "agent.yml"), "new")
			writeRuntimeAssemblerFile(t, filepath.Join(stable, "agent.yml"), "old")
			err := publishAgentCandidateWithRename(candidate, stable, true, func(stage, a, b string) error {
				if stage == "publish" || (stage == "rollback" && failRollback) {
					return syscall.Errno(5)
				}
				return os.Rename(a, b)
			})
			if !errors.Is(err, syscall.Errno(5)) {
				t.Fatalf("err=%v", err)
			}
			oldPath := stable
			expected := "rollback=restored"
			if failRollback {
				oldPath = candidate + ".previous"
				expected = "rollback=failed"
			}
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("err=%v", err)
			}
			assertRuntimeAssemblerContent(t, filepath.Join(oldPath, "agent.yml"), "old")
			assertRuntimeAssemblerContent(t, filepath.Join(candidate, "agent.yml"), "new")
		})
	}
}

func TestRuntimeAgentPublicationReplacesAndSkipsUnchanged(t *testing.T) {
	root := t.TempDir()
	candidate, stable := filepath.Join(root, "candidate"), filepath.Join(root, "stable")
	writeRuntimeAssemblerFile(t, filepath.Join(candidate, "agent.yml"), "new")
	writeRuntimeAssemblerFile(t, filepath.Join(stable, "agent.yml"), "old")
	if err := publishAgentCandidate(candidate, stable, true); err != nil {
		t.Fatal(err)
	}
	assertRuntimeAssemblerContent(t, filepath.Join(stable, "agent.yml"), "new")
	writeRuntimeAssemblerFile(t, filepath.Join(candidate, "agent.yml"), "new")
	if err := publishAgentCandidateWithRename(candidate, stable, true, func(string, string, string) error { t.Fatal("unchanged tree renamed"); return nil }); err != nil {
		t.Fatal(err)
	}
}
