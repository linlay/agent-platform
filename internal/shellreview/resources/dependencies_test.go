package resources

import (
	"agent-platform/internal/shellreview/types"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writableDependency(string) (DependencyPermission, error) {
	return DependencyPermission{Writable: true}, nil
}
func TestGuestDependencyNeverReadsHostAndCachesOnlyOneReview(t *testing.T) {
	file := filepath.Join(t.TempDir(), "host-only.cfg")
	if err := os.WriteFile(file, []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	deps := []types.Dep{{Kind: types.ConfigFile, Path: file}}
	x := types.BashExecution{Cwd: "/guest"}
	first := NewDependencyInspector(true, nil, writableDependency).Review(deps, x, false)
	if first.State != types.Unresolved || !first.Unsafe {
		t.Fatalf("missing guest probe borrowed Host: %+v", first)
	}
	if err := os.WriteFile(file, []byte("changed-host"), 0600); err != nil {
		t.Fatal(err)
	}
	second := NewDependencyInspector(true, nil, writableDependency).Review(deps, x, false)
	if first.SHA256 != second.SHA256 {
		t.Fatal("guest version was computed from Host bytes")
	}
	calls := 0
	env := &types.BashEnvironment{InspectConfig: func(string) (types.FileInspection, error) {
		calls++
		return types.FileInspection{}, errors.New("failed")
	}}
	inspector := NewDependencyInspector(true, env, writableDependency)
	for range 2 {
		if result := inspector.Review(deps, x, false); result.State != types.Unresolved {
			t.Fatal(result)
		}
	}
	if calls != 1 {
		t.Fatalf("same review retried failed probe %d times", calls)
	}
	NewDependencyInspector(true, env, writableDependency).Review(deps, x, false)
	if calls != 2 {
		t.Fatal("new review reused stale guest result")
	}
}
func TestDependencyCandidatePrecedenceAndClearedVariables(t *testing.T) {
	var paths []string
	env := &types.BashEnvironment{AccountHome: func() (string, error) { return "/account", nil }, InspectConfig: func(path string) (types.FileInspection, error) {
		paths = append(paths, path)
		return types.FileInspection{}, nil
	}}
	deps := []types.Dep{{Kind: types.ConfigFile, Candidates: []types.ConfigCandidate{{Variable: "WGETRC", StopWhenSet: true}, {Variable: "HOME", Relative: ".wgetrc", StopWhenSet: true, EmptyPath: "/.wgetrc"}, {NativeHome: true, Relative: ".wgetrc"}}}}
	inspect := func(vars map[string]string) []string {
		paths = nil
		NewDependencyInspector(true, env, writableDependency).Review(deps, types.BashExecution{Cwd: "/cwd", Env: vars}, false)
		return append([]string(nil), paths...)
	}
	for _, tc := range []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{"WGETRC": "rc", "HOME": "/home"}, "/cwd/rc"},
		{map[string]string{"WGETRC": "", "HOME": "/home"}, "/home/.wgetrc"},
		{map[string]string{"HOME": ""}, "/.wgetrc"},
		{map[string]string{}, "/account/.wgetrc"},
	} {
		if got := strings.Join(inspect(tc.vars), ","); got != tc.want {
			t.Fatalf("%v: paths=%q want %q", tc.vars, got, tc.want)
		}
	}
}
func TestDependencyProbeCannotOverwriteHardViolation(t *testing.T) {
	probes := 0
	inspector := NewDependencyInspector(true, &types.BashEnvironment{InspectConfig: func(string) (types.FileInspection, error) {
		probes++
		return types.FileInspection{}, errors.New("probe failed")
	}}, func(path string) (DependencyPermission, error) {
		if path == "/protected" {
			return DependencyPermission{HardReason: "protected resource"}, nil
		}
		return DependencyPermission{}, nil
	})
	got := inspector.Review([]types.Dep{{Kind: types.ConfigFile, Path: "/protected"}, {Kind: types.ConfigFile, Path: "/other"}}, types.BashExecution{Cwd: "/"}, false)
	if got.State != types.HardViolation || got.Reason != "protected resource" || probes != 0 {
		t.Fatalf("later failure erased hard violation or started probe: %+v calls=%d", got, probes)
	}
}
func TestHostConfigTypeLimitAndByteFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := inspectHostConfig(path)
	if err != nil || !first.Exists || len(first.SHA256) != 64 {
		t.Fatalf("%+v %v", first, err)
	}
	if err := os.WriteFile(path, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := inspectHostConfig(path)
	if err != nil || first.SHA256 == second.SHA256 {
		t.Fatal("bytes did not change dependency version")
	}
	if _, err := inspectHostConfig(filepath.Dir(path)); err == nil {
		t.Fatal("directory treated as regular config")
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxDependencyBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := inspectHostConfig(path); err == nil {
		t.Fatal("oversized dependency was hashed as complete")
	}
}
