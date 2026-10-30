package accesspolicy

import (
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
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
