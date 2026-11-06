package shellreview

import (
	"runtime"
	"testing"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

// find -exec whose commands only read is reviewed like find over its roots;
// any write, unanalyzed code or unmodeled option keeps the old requirements.
func TestReadOnlyFindExecNeedsNoExecutionApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix find fixtures")
	}
	vars, environment := fakeSystemPrograms(t, "find", "stat", "ls", "cat", "rm", "wc", "awk", "sort", "head", "sh", "python3")
	readOnly := []string{
		`find . -name '*.txt' -exec cat '{}' ';'`,
		`find . -maxdepth 3 -type f \( -iname '*.mp4' -o -iname '*.mov' \) -exec stat -f '%z|%Sm|%N' -t '%Y-%m-%d %H:%M' {} \;`,
		`find . -type f -size +20M -exec ls -lh {} + 2>/dev/null | awk '{print $5, $9}' | sort -hr | head -10`,
		`find a b -exec wc -l {} +`,
	}
	kept := []string{
		`find . -exec rm {} \;`,
		`find . -exec sh -c 'cat "$1"' _ {} \;`,
		`find . -exec python3 task.py {} \;`,
		`find . -exec cat --made-up {} \;`,
		`find . -execdir cat {} \;`,
		`find . -exec cat {}`,
		`find . -exec cat {} \; -exec rm {} \;`,
		`find . -exec cat {} \; ; find . -exec rm {} \;`,
	}
	want := map[string]Decision{AccessLevelAskForApproval: DecisionRequiresApproval, AccessLevelAutoApprove: DecisionAutoApproved, AccessLevelFullAccess: DecisionAutoApproved}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		run := func(command string) Decision {
			return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars, environment, nil).Decision
		}
		for _, command := range readOnly {
			if got := run(command); got != DecisionAllow {
				t.Errorf("%s %q: got %s, want allow", level, command, got)
			}
		}
		for _, command := range kept {
			if got := run(command); got != want[level] {
				t.Errorf("%s %q: got %s, want %s", level, command, got, want[level])
			}
		}
		// Inner paths outside the roots keep their own review.
		if got := run(`find . -exec cat /outside-find/x {} \;`); got == DecisionAllow && level != AccessLevelFullAccess {
			t.Errorf("%s: an outside inner path was not reviewed", level)
		}
	}
}
