package config

import "testing"

func TestLegacyDefaultPolicyRetainsRestrictionsUnderApprovalName(t *testing.T) {
	cfg := normalizeAccessPolicyConfig(AccessPolicyConfig{Levels: map[string]AccessPolicyLevelConfig{
		"default":      {ReadonlyRoots: []string{"/custom-readonly"}, Approvals: AccessPolicyApprovalConfig{WriteOutsideRoots: "block"}},
		"auto_approve": {Inherit: "default"},
	}})
	if _, ok := cfg.Levels["default"]; ok {
		t.Fatal("request selector remained a policy level")
	}
	approval := cfg.Levels["ask_for_approval"]
	if len(approval.ReadonlyRoots) != 1 || approval.ReadonlyRoots[0] != "/custom-readonly" || approval.Approvals.WriteOutsideRoots != "block" || cfg.Levels["auto_approve"].Inherit != "ask_for_approval" {
		t.Fatalf("legacy restrictions were lost: %#v", cfg)
	}
}
