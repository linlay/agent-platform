package hitl

import "testing"

func TestCheckRulesUsesASTCommandsAcrossPipeline(t *testing.T) {
	byCmd := map[string][]FlatRule{
		"grep": {
			{
				RuleKey:     "grep-foo",
				Command:     "grep",
				Match:       "foo",
				MatchTokens: []string{"foo"},
				Level:       1,
			},
		},
	}
	result := checkRules(byCmd, "git status | grep foo", 0)
	if !result.Intercepted {
		t.Fatalf("expected AST pipeline command to be intercepted, got %#v", result)
	}
	if result.ParsedCommand.BaseCommand != "grep" {
		t.Fatalf("expected grep command, got %#v", result.ParsedCommand)
	}
}

func TestCheckRulesFallsBackWhenASTTooComplex(t *testing.T) {
	byCmd := map[string][]FlatRule{
		"git": {
			{
				RuleKey:     "git-status",
				Command:     "git",
				Match:       "status",
				MatchTokens: []string{"status"},
				Level:       1,
			},
		},
	}
	result := checkRules(byCmd, "git status {a,b}", 0)
	if !result.Intercepted {
		t.Fatalf("expected legacy fallback interception, got %#v", result)
	}
}
