package policy

import (
	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/types"
	"testing"
)

func TestResourceOffsetCannotEraseHardBlocksOrUncertainty(t *testing.T) {
	for _, level := range []string{contracts.AccessLevelAskForApproval, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		session := contracts.QuerySession{AccessLevel: level}
		cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{level: {ReadonlyRoots: []string{"/dev/null"}}}}
		reviewer := New(cfg, session, "echo >/dev/null")
		path := reviewer.Path(accesspolicy.WriteAccess, "/dev/null", false)
		got := reviewer.ResourceRequirements(path, types.ResourceResult{State: types.Known, NullDevice: true}, "/dev/null")
		if len(got) != 1 || !got[0].Blocked() {
			t.Fatalf("readonly offset at %s: %+v", level, got)
		}
	}
	reviewer := New(config.AccessPolicyConfig{}, contracts.QuerySession{AccessLevel: contracts.AccessLevelAskForApproval}, "echo >/dev/null")
	path := reviewer.Path(accesspolicy.WriteAccess, "/dev/null", false)
	got := reviewer.ResourceRequirements(path, types.ResourceResult{State: types.Unresolved, Reason: "probe failed"}, "/dev/null")
	if len(got) != 2 || got[0].Fingerprint != path.Fingerprint || got[1].RuleKey != "bash-access:complex" {
		t.Fatalf("uncertainty erased path leaf: %+v", got)
	}
	got = reviewer.ResourceRequirements(path, types.ResourceResult{State: types.HardViolation, NullDevice: true, Reason: "protected"}, "/dev/null")
	if len(got) != 1 || !got[0].Blocked() {
		t.Fatalf("hard violation offset: %+v", got)
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{contracts.AccessLevelAskForApproval: {Approvals: config.AccessPolicyApprovalConfig{WriteOutsideRoots: "block"}}}}
	reviewer = New(cfg, contracts.QuerySession{AccessLevel: contracts.AccessLevelAskForApproval}, "echo >/dev/null")
	got = reviewer.ResourceRequirements(reviewer.Path(accesspolicy.WriteAccess, "/dev/null", false), types.ResourceResult{State: types.Known, NullDevice: true}, "/dev/null")
	if len(got) != 1 || !got[0].Blocked() {
		t.Fatal("explicit admin block was offset")
	}
}
