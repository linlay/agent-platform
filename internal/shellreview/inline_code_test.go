package shellreview

import (
	"runtime"
	"strings"
	"testing"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
)

// Literal program text given to a system interpreter is as visible as a script
// this Run wrote, so it runs without execution approval at every level. The
// surrounding arguments, redirects and Workspace editing still apply.
func TestInlineInterpreterCodeIsLikeAnAuthoredScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix interpreter fixtures")
	}
	vars, environment := fakeSystemPrograms(t, "python3", "node", "perl", "ruby", "cat", "head", "echo", "env", "bash")
	inline := []string{
		`python3 -c "print(1)"`,
		`python3 -I -c 'import json; print(json.dumps({}))'`,
		"python3 - <<'EOF'\nprint(1)\nEOF",
		"python3 <<'EOF'\nimport os\nprint(os.getcwd(), \"$HOME\")\nEOF",
		"python3 <<EOF\nprint(1)\nEOF",
		`python3 <<< 'print(1)'`,
		`node -e 'console.log(1)'`,
		`perl -ne 'print if /a/' notes.txt`,
		`perl -lane 'print $F[0]' notes.txt`,
		`ruby -e 'puts 1'`,
		`python3 -c "print(1)" | head -2`,
		`echo x | python3 -c 'import sys; print(sys.stdin.read())'`,
		`bash -c 'python3 -c "print(1)"'`,
	}
	opaque := []string{
		`python3 -c "print('$HOME')"`,
		`python3 -c "$(cat task.py)"`,
		`python3 task.py`,
		`python3 -m http.server`,
		"python3 - <<EOF\nprint(\"$HOME\")\nEOF",
		`cat task.py | python3`,
		`python3 - < task.py`,
		`perl -pi -e 's/a/b/' notes.txt`,
		`ruby -E utf-8 task.rb`,
		`env python3 -c 'print(1)'`,
	}
	want := map[string]Decision{AccessLevelAskForApproval: DecisionRequiresApproval, AccessLevelAutoApprove: DecisionAutoApproved, AccessLevelFullAccess: DecisionAllow}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		run := func(session QuerySession, command string) shellplan.BashPlan {
			return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, session, command, "", vars, environment, nil)
		}
		for _, command := range inline {
			p := run(f.session, command)
			if p.Decision != DecisionAllow || !hasLeaf(p, "bash-access:inline-code") {
				t.Errorf("%s %q: want inline-code allow, got %+v", level, command, p)
			}
		}
		for _, command := range opaque {
			p := run(f.session, command)
			if p.Decision != want[level] || hasLeaf(p, "bash-access:inline-code") {
				t.Errorf("%s %q: want %s without inline-code, got %+v", level, command, want[level], p)
			}
		}
		// Visible outside paths and redirects keep their own review.
		for _, command := range []string{`python3 -c 'print(1)' /outside-inline/x`, `python3 -c 'print(1)' > /outside-inline/out`} {
			if p := run(f.session, command); p.Decision == DecisionAllow && level != AccessLevelFullAccess {
				t.Errorf("%s %q: outside path was not reviewed: %+v", level, command, p)
			}
		}
		readonly := f.session
		readonly.WorkspaceReadOnly = true
		if p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, readonly, `python3 -c 'print(1)'`, f.workspace, vars, environment, nil); !p.Blocked() {
			t.Errorf("%s: editing=false still forbids unanalyzed code in the Workspace: %+v", level, p)
		}
	}
}

func hasLeaf(p shellplan.BashPlan, rule string) bool {
	if strings.HasPrefix(p.RuleKey, rule) {
		return true
	}
	for _, r := range p.Requirements {
		if hasLeaf(r, rule) {
			return true
		}
	}
	return false
}
