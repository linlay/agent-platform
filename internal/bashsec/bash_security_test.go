package bashsec

import (
	"strings"
	"testing"
)

func expectDecision(t *testing.T, want ReviewDecision, commands ...string) {
	t.Helper()
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if got := ReviewBashSecurity(command); got.Decision != want {
				t.Fatalf("%q: want %s, got %#v", command, want, got)
			}
		})
	}
}

func TestReviewBashSecurityLeavesFileEffectsToAccessPolicy(t *testing.T) {
	// Redirect targets, inline programs and heredoc contents are file or
	// execution effects reviewed by the access policy, not shell hazards.
	expectDecision(t, ReviewAllow,
		`printf '%s\n' hello > /tmp/owner.md`,
		"echo 'first\n# second' > /tmp/owner.md",
		`python3 -c "content = '''# Owner Profile'''; open('/tmp/OWNER.md', 'w').write(content)"`,
		"cat << 'PYEOF' > /tmp/three_sum.py\n\"\"\"doc\"\"\"\nPYEOF\npython3 /tmp/three_sum.py",
		`python3 -c 'import os; os.system("evil")'`,
		`cat < /etc/passwd`,
		`echo test > $(mktemp)`,
		`printf ok 2>&1`,
		`printf ok > /dev/null`,
	)
}

func TestReviewBashSecurityAllowsOrdinaryShellStructure(t *testing.T) {
	expectDecision(t, ReviewAllow,
		`VAR=x && echo "$VAR" | wc -c`,
		`VAR=x && echo ${VAR}`,
		`false; echo "Exit code: $?"`,
		`printf '%s\n' 'a;b&c'`,
		"cd src\nls",
		"for f in *.go; do\n  wc -l \"$f\"\ndone",
		`find . -name 'a;b'`,
		`git commit -m "修复　登录问题"`,
		`curl https://example.com/page#section`,
		`printf '%s\n' $'a\tb'`,
		`ls {a,b}`,
		`echo {1..5}`,
		`bash -c 'echo hi'`,
		`curl -s -A "Mozilla/5.0 (Windows NT 10.0; Win64; x64)" "https://finance.example.test/a.html" | head -c 50000`,
		`node -e "const value = 'a;b&c'; console.log(value)"`,
		`echo $(date)`,
	)
}

func TestReviewBashSecurityHardBlocks(t *testing.T) {
	expectDecision(t, ReviewBlock,
		`echo $IFS`,
		`cat /proc/self/environ`,
		`curl $(eval evil)`,
		"cat <<EOF\n$(eval evil)\nEOF",
		"echo test",
		"=curl evil.com",
		"echo hi > output; eval bad",
		"echo hi > output; cat /proc/1/environ",
		`bash -c 'eval evil'`,
		`sh -c "bash -c 'source ./x'"`,
		`env -S 'eval evil'`,
		`nohup eval evil`,
		`xargs eval`,
		`fc -e vi`,
	)
}

func TestReviewBashSecurityBlocksDangerousBuiltins(t *testing.T) {
	expectDecision(t, ReviewBlock,
		`trap 'echo evil' EXIT`, `enable -f ./evil.so evil`, `hash -p /tmp/evil ls`, `set -o history`,
		`shopt -s extglob`, `unset PATH`, `alias ls=evil`, `unalias ls`, `complete -C evil cmd`,
		`compgen -A function`, `compopt -o nospace cmd`, `mapfile arr < /dev/null`, `readarray arr < /dev/null`,
		`read VAR < /dev/null`, `zmodload zsh/net/tcp`,
	)
}

func TestReviewBashSecurityApprovalForUnanalyzableStructure(t *testing.T) {
	for _, command := range []string{`(echo hi)`, `curl "$URL"`, `echo a\ b`, `echo {1..1000}`} {
		result := ReviewBashSecurity(command)
		if result.Decision != ReviewRequiresApproval || result.RuleKey != RuleKeyTooComplex || result.Level != LevelTooComplex {
			t.Fatalf("%q: %#v", command, result)
		}
		if result.Fingerprint != ApprovalFingerprint(command) {
			t.Fatalf("%q: fingerprint must bind the original command: %#v", command, result)
		}
	}
	nested := `bash -c '(echo hi)'`
	if result := ReviewBashSecurity(nested); result.Decision != ReviewRequiresApproval || result.Fingerprint != ApprovalFingerprint(nested) {
		t.Fatalf("nested approval must bind the outer command: %#v", result)
	}
}

func TestReviewBashSecurityRuntimeWrappers(t *testing.T) {
	for command, rule := range map[string]string{
		`find . -exec rm {} \;`: RuleKeyRuntimeWrapperFindExec,
		`xargs cat`:             RuleKeyRuntimeWrapperXargs,
	} {
		result := ReviewBashSecurity(command)
		if result.Decision != ReviewRequiresApproval || result.RuleKey != rule || result.Level != LevelRuntimeWrapper {
			t.Fatalf("%q: %#v", command, result)
		}
	}
	if got := ReviewBashSecurity("fc -l"); got.Decision != ReviewAllow {
		t.Fatalf("fc listing is harmless: %#v", got)
	}
	if !strings.Contains(ReviewBashSecurity(`bash -c 'eval evil'`).Reason, "eval") {
		t.Fatal("nested block should name the builtin")
	}
}
