package hitl

import "testing"

func TestEveryBusinessHookMatchesThroughWrappersAndGitFlags(t *testing.T) {
	rules := []FlatRule{
		{Command: "git", Match: "push", MatchTokens: []string{"push"}, RuleKey: "broad", Level: 1, Title: "publish"},
		{Command: "git", Match: "push origin", MatchTokens: []string{"push", "origin"}, RuleKey: "specific", Level: 2, Title: "publish to origin"},
	}
	checker := &SkillChecker{rules: rules, byCmd: buildIndexes(rules)}
	for _, cmd := range []string{"git push origin main", "env MODE=test git -C repo -c color.ui=false push origin main", "command /usr/bin/git.exe --git-dir=repo/.git push origin main", "timeout 10 git push origin main"} {
		matches := checker.CheckAll(cmd)
		if len(matches) != 2 {
			t.Fatalf("%s: missed hooks: %+v", cmd, matches)
		}
		result := CombineRequirements(cmd, matches)
		if !result.Intercepted || len(result.Requirements) != 2 || len(RequirementMetadata(result)) != 2 {
			t.Fatalf("lost requirements: %+v", result)
		}
	}
}

func TestConflictingFormsCannotSilentlyAuthorizeEachOther(t *testing.T) {
	match := CombineRequirements("cli send", []InterceptResult{
		{Intercepted: true, Rule: FlatRule{RuleKey: "approval", Mode: "approval"}},
		{Intercepted: true, Rule: FlatRule{RuleKey: "form", Mode: "form"}},
	})
	if match.Conflict == "" {
		t.Fatal("form requirement was dropped")
	}
}

func TestSingleUseApprovalRejectsForgedRunScope(t *testing.T) {
	args := map[string]any{"approvals": []any{map[string]any{
		"id": "complex-command", "options": []any{map[string]any{"decision": "approve"}},
	}}}
	if _, err := NormalizeApproval(args, []any{map[string]any{"id": "complex-command", "decision": "approve_rule_run"}}); err == nil {
		t.Fatal("client enlarged a single-use approval to run scope")
	}
	if _, err := NormalizeApproval(args, []any{map[string]any{"id": "complex-command", "decision": "approve"}}); err != nil {
		t.Fatal(err)
	}
}
