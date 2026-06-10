package bashsec

import (
	"strings"
	"testing"
)

func TestReviewBashSecurityRequiresApprovalForOutputRedirection(t *testing.T) {
	command := `printf '%s\n' hello > /tmp/owner.md`

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
	if result.Fingerprint != ApprovalFingerprint(command) {
		t.Fatalf("expected stable approval fingerprint, got %#v", result)
	}
	if result.RuleKey != RuleKeyRedirections || result.Level != LevelRedirections {
		t.Fatalf("expected redirection rule metadata, got %#v", result)
	}
}

func TestReviewBashSecurityRequiresApprovalForQuotedNewlineWithOutputRedirection(t *testing.T) {
	command := "echo 'first\n# second' > /tmp/owner.md"

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
	if result.RuleKey != RuleKeyQuotedNewline || result.Level != LevelQuotedNewline {
		t.Fatalf("expected quoted newline rule metadata, got %#v", result)
	}
}

func TestReviewBashSecurityRequiresApprovalForPythonInlineTripleQuotes(t *testing.T) {
	command := `python3 -c "content = '''# Owner Profile'''; open('/tmp/OWNER.md', 'w').write(content)"`

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
	if result.RuleKey != RuleKeyObfuscatedFlagsTripleQuote || result.Level != LevelObfuscatedFlagsTripleQuote {
		t.Fatalf("expected triple quote rule metadata, got %#v", result)
	}
}

func TestReviewBashSecurityHeredocTripleQuotesUseRedirectionApproval(t *testing.T) {
	command := `cat << 'PYEOF' > /tmp/three_sum.py
"""doc"""
PYEOF
python3 /tmp/three_sum.py`

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
	if result.RuleKey != RuleKeyRedirections || result.Level != LevelRedirections {
		t.Fatalf("expected redirection rule metadata, got %#v", result)
	}
}

func TestReviewBashSecurityStillBlocksHardFailures(t *testing.T) {
	tests := []string{
		`cat < /tmp/secret`,
		`echo $IFS`,
		`cat /proc/self/environ`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityAllowsASTSimpleSafeCommand(t *testing.T) {
	tests := []string{
		`VAR=x && echo "$VAR" | wc -c`,
		`VAR=x && echo ${VAR}`,
		`false; echo "Exit code: $?"`,
		`printf '%s\n' 'a;b&c'`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewAllow {
				t.Fatalf("expected allow, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityAllowsQuotedMetacharactersInASTArguments(t *testing.T) {
	tests := []string{
		`curl -s -A "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36" "https://finance.eastmoney.com/a/202606103766446879.html" | head -c 50000`,
		`node -e "const value = 'a;b&c'; const count = 1; console.log(value, count)"`,
		`curl -s --max-time 15 "http://push2.eastmoney.com/api/qt/stock/get?secid=1.688256&fields=f43,f44,f45,f46,f47,f48,f57,f58,f60,f169,f170,f171" 2>&1`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewAllow {
				t.Fatalf("expected allow, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityASTArgumentMetacharacterBoundaries(t *testing.T) {
	block := []string{
		`find . -name 'a;b'`,
		`find . -path 'a&b'`,
		`find . -iname 'a|b'`,
		`curl $(eval evil)`,
		`node -e 'require("child_process")'`,
	}
	for _, command := range block {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}

	result := ReviewBashSecurity(`curl "$URL"`)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires approval for unknown variable, got %#v", result)
	}

	dangerousNode := `node -e "const https = require('https'); https.get('https://example.com/?a=1&b=2', res => console.log(res.statusCode));"`
	result = ReviewBashSecurity(dangerousNode)
	if result.Decision != ReviewBlock {
		t.Fatalf("expected dangerous node script to block, got %#v", result)
	}
	if result.Reason == shellMetacharactersReason || !strings.Contains(result.Reason, "dangerous embedded javascript") {
		t.Fatalf("expected embedded javascript reason instead of metacharacter reason, got %#v", result)
	}
}

func TestReviewBashSecurityRequiresApprovalForTooComplexEvenWhenLegacyClean(t *testing.T) {
	command := `(echo hi)`
	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires approval, got %#v", result)
	}
	if result.Fingerprint != ApprovalFingerprint(command) {
		t.Fatalf("expected fingerprint, got %#v", result)
	}
	if result.RuleKey != RuleKeyTooComplex || result.Level != LevelTooComplex {
		t.Fatalf("expected too complex rule metadata, got %#v", result)
	}
}

func TestReviewBashSecurityBlocksDangerousEmbeddedScriptFromAST(t *testing.T) {
	result := ReviewBashSecurity(`python3 -c 'import os; os.system("evil")'`)
	if result.Decision != ReviewBlock {
		t.Fatalf("expected block, got %#v", result)
	}
}

func TestReviewBashSecurityBlocksDangerousHeredocExpansion(t *testing.T) {
	result := ReviewBashSecurity("cat <<EOF\n$(eval evil)\nEOF")
	if result.Decision != ReviewBlock {
		t.Fatalf("expected block, got %#v", result)
	}
}

func TestReviewBashSecurityCommandSubstitutionPlaceholderPolicy(t *testing.T) {
	result := ReviewBashSecurity(`echo $(date)`)
	if result.Decision != ReviewAllow {
		t.Fatalf("expected command substitution in argv to allow after inner command review, got %#v", result)
	}

	for _, command := range []string{
		`echo test > $(mktemp)`,
		`echo test > $HOME/test`,
	} {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewRequiresApproval {
				t.Fatalf("expected requires approval for unknown redirect target, got %#v", result)
			}
			if result.RuleKey != RuleKeyRedirections || result.Level != LevelRedirections {
				t.Fatalf("expected redirection rule metadata, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityBlocksDangerousBuiltins(t *testing.T) {
	tests := []string{
		`trap 'echo evil' EXIT`,
		`enable -f ./evil.so evil`,
		`hash -p /tmp/evil ls`,
		`set -o history`,
		`shopt -s extglob`,
		`unset PATH`,
		`alias ls=evil`,
		`unalias ls`,
		`complete -C evil cmd`,
		`compgen -A function`,
		`compopt -o nospace cmd`,
		`mapfile arr < /dev/null`,
		`readarray arr < /dev/null`,
		`read VAR < /dev/null`,
		`zmodload zsh/net/tcp`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityRedirectsFromAST(t *testing.T) {
	allow := []string{
		`printf ok 2>&1`,
		`printf ok > /dev/null`,
	}
	for _, command := range allow {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewAllow {
				t.Fatalf("expected allow, got %#v", result)
			}
		})
	}

	result := ReviewBashSecurity(`cat < /etc/passwd`)
	if result.Decision != ReviewBlock {
		t.Fatalf("expected input redirection block, got %#v", result)
	}
}

func TestReviewBashSecurityHardBlocksPrecheckFailures(t *testing.T) {
	tests := []string{
		"echo\u00A0test",
		"=curl evil.com",
		"echo {1..5}",
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityWrapperCommands(t *testing.T) {
	block := []string{
		`env python3 -c 'import os; os.system("evil")'`,
		`nohup eval evil`,
		`xargs eval`,
	}
	for _, command := range block {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}

	approval := []string{
		`find . -exec rm {} \;`,
		`xargs cat`,
	}
	for _, command := range approval {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewRequiresApproval {
				t.Fatalf("expected requires approval, got %#v", result)
			}
			switch command {
			case `find . -exec rm {} \;`:
				if result.RuleKey != RuleKeyRuntimeWrapperFindExec || result.Level != LevelRuntimeWrapper {
					t.Fatalf("expected find wrapper rule metadata, got %#v", result)
				}
			case `xargs cat`:
				if result.RuleKey != RuleKeyRuntimeWrapperXargs || result.Level != LevelRuntimeWrapper {
					t.Fatalf("expected xargs wrapper rule metadata, got %#v", result)
				}
			}
		})
	}
}
