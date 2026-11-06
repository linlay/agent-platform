package shellreview

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

// A for loop over static items is reviewed once per value, so its body gets
// the same path review as writing each value out by hand.
func TestForLoopValuesGetPathReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix glob fixtures")
	}
	vars, environment := fakeSystemPrograms(t, "cat", "ls", "echo")
	f := newCapabilityFixture(t, AccessLevelAskForApproval)
	outside := t.TempDir()
	for _, dir := range []string{filepath.Join(f.workspace, "sub"), filepath.Join(outside, "one"), filepath.Join(outside, "two")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a.txt", "b c.txt", "sub/d.txt"} {
		if err := os.WriteFile(filepath.Join(f.workspace, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(f.workspace, "sub", "escape")); err != nil {
		t.Fatal(err)
	}
	run := func(command string) Decision {
		return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, environment, nil).Decision
	}
	for command, want := range map[string]Decision{
		`for f in a.txt "b c.txt"; do cat "$f"; done`:                                       DecisionAllow,
		`for f in *.txt; do echo "== $f"; cat "$f"; done`:                                   DecisionAllow,
		`cd sub && for f in *.txt; do cat "$f"; done`:                                       DecisionAllow,
		"for d in " + outside + "/*/; do echo \"=== $d\"; ls -A \"$d\"; done":               DecisionRequiresApproval,
		`cd sub && for f in *; do cat "$f"; done`:                                           DecisionRequiresApproval, // escape -> outside
		`for f in a.txt; do cat $f; done`:                                                   DecisionRequiresApproval, // unquoted: syntax review
		`for f in $(ls); do cat "$f"; done`:                                                 DecisionRequiresApproval,
		`for d in sub sub; do cd "$d"; cat d.txt; done`:                                     DecisionRequiresApproval,
		"for f in " + strings.Repeat("a.txt ", maxTestLoopValues) + "; do cat \"$f\"; done": DecisionRequiresApproval,
	} {
		if got := run(command); got != want {
			t.Errorf("%s: got %s, want %s", command, got, want)
		}
	}
	// Outside values are reviewed as paths, not as an unresolvable variable.
	p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, "for d in "+outside+"/*/; do ls -A \"$d\"; done", f.workspace, vars, environment, nil)
	if hasLeaf(p, "bash-access:complex") || !hasLeaf(p, "bash-access:path:") {
		t.Errorf("outside glob values need path approval only: %+v", p)
	}
}

const maxTestLoopValues = 65
