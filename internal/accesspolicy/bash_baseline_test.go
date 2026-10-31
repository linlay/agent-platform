package accesspolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// These normalized cases lock the observable baseline without storing hashes
// of machine-specific temp roots, PATH values or system installations.
func TestBashReviewPolicyBaseline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix system-command baseline; Windows grammar has a separate gate")
	}
	f := newCapabilityFixture(t, AccessLevelAskForApproval)
	for _, tc := range []struct {
		command  string
		decision Decision
		rule     string
	}{
		{"echo ok", DecisionAllow, "bash-access"},
		{"cat notes.txt", DecisionAllow, "bash-access"},
		{"printf ok > notes.txt", DecisionAllow, "bash-access"},
		{"rm -r build", DecisionRequiresApproval, "bash-access:destructive"},
		{"curl -X DELETE https://example.invalid/item", DecisionRequiresApproval, "bash-access:remote"},
		{"cat /outside/read", DecisionRequiresApproval, "bash-access:path:"},
	} {
		p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, tc.command, f.workspace, nil)
		if p.Decision != tc.decision || !strings.HasPrefix(p.RuleKey, tc.rule) {
			t.Fatalf("%s: want %s / %s, got %+v", tc.command, tc.decision, tc.rule, p)
		}
		fresh := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, tc.command, f.workspace, nil)
		if p.Fingerprint != fresh.Fingerprint {
			t.Fatalf("fixed-fixture fingerprint is unstable: %s", tc.command)
		}
	}
}

// fakeSystemPrograms makes names resolve, through a private PATH, to recorded
// system installations, so cases do not depend on what the host has installed.
func fakeSystemPrograms(t *testing.T, names ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("fake\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c, err := pathutil.Canonicalize(p)
		if err != nil {
			t.Fatal(err)
		}
		old, had := systemExecutables[name]
		systemExecutables[name] = c.Key
		t.Cleanup(func() {
			if had {
				systemExecutables[name] = old
			} else {
				delete(systemExecutables, name)
			}
		})
	}
	return map[string]string{"PATH": dir}
}

// Unrecognized options and config sources outside argv must not inherit the
// approval-free path of fully analyzed commands.
func TestBashReviewOptionsAndImplicitConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix system-command baseline; Windows grammar has a separate gate")
	}
	vars := fakeSystemPrograms(t, "sort", "curl", "wget", "rg", "tar", "ls", "rm", "env")
	const url = "https://example.invalid/x"
	opaque := []string{
		"sort -S 1K --compress-program=sh data.txt",
		"curl -K ./cfg " + url,
		"curl --config=./cfg " + url,
		"wget --config=./cfg " + url,
		"CURL_HOME=. curl " + url,
		"CURL_HOME=. curl -s -q " + url, // -q only counts as the first argument
		"env CURL_HOME=. curl " + url,
		"export CURL_HOME=. && curl " + url,
		"WGETRC=./w wget -O out.html " + url,
		"RIPGREP_CONFIG_PATH=./rgrc rg foo",
		"TAR_OPTIONS=--to-command=sh tar -xf a.tar",
	}
	complex := []string{
		"curl host.invalid/path",
		"ls --made-up-flag",
	}
	allowed := []string{
		"sort data.txt",
		"CURL_HOME=. curl -q " + url,
		"WGETRC=./w wget --no-config -O out.html " + url,
		"RIPGREP_CONFIG_PATH=./rgrc rg --no-config foo",
		"curl --version",
		"curl -s --socks5-hostname 127.0.0.1:1081 " + url,
		"tar -xf a.tar",
	}
	want := map[string]Decision{AccessLevelAskForApproval: DecisionRequiresApproval, AccessLevelAutoApprove: DecisionAutoApproved, AccessLevelFullAccess: DecisionAllow}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		check := func(command string, decision Decision, rule string) {
			t.Helper()
			p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, f.workspace, vars)
			if decision == DecisionAllow {
				rule = "bash-access" // allowed plans do not keep a leaf rule
			}
			if p.Decision != decision || !strings.HasPrefix(p.RuleKey, rule) {
				t.Fatalf("%s %s: want %s / %s, got %+v", level, command, decision, rule, p)
			}
		}
		for _, command := range opaque {
			check(command, want[level], "bash-access:execution:")
		}
		for _, command := range complex {
			check(command, want[level], "bash-access:complex")
		}
		for _, command := range allowed {
			check(command, DecisionAllow, "bash-access")
		}
		// An inherited or run.env value is the same config source as an inline one.
		env := map[string]string{"PATH": vars["PATH"], "TAR_OPTIONS": "--to-command=sh"}
		if p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, "tar -xf a.tar", f.workspace, env); p.Decision != want[level] {
			t.Fatalf("%s: environment TAR_OPTIONS: %+v", level, p)
		}
	}

	f := newCapabilityFixture(t, AccessLevelAskForApproval)
	p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, "ls --made-up-flag", f.workspace, vars)
	if RuleReusable(p.RuleKey) {
		t.Fatalf("an unrecognized option is approved once, not for the run: %+v", p)
	}
	// Known effects stay beside the unknown option.
	p = ReviewBashCommand(config.AccessPolicyConfig{}, f.session, "rm -r --made-up-flag build", f.workspace, vars)
	rules := map[string]bool{}
	for _, leaf := range p.Requirements {
		kind, _, _ := strings.Cut(strings.TrimPrefix(leaf.RuleKey, "bash-access:"), ":")
		rules[kind] = true
	}
	if !p.RequiresApproval() || !rules["destructive"] || !rules["complex"] {
		t.Fatalf("destructive and complex requirements must both remain: %+v", p)
	}

	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		f.session.WorkspaceReadOnly = true
		for _, command := range []string{"curl -K ./cfg " + url, "CURL_HOME=. curl " + url} {
			if p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, "@workspace", vars); !p.Blocked() {
				t.Fatalf("%s editing=false: config-driven curl inside the workspace must be blocked: %s %+v", level, command, p)
			}
		}
	}
}
