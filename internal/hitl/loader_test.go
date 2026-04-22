package hitl

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadRulesFromDir(t *testing.T) {
	root := t.TempDir()
	content := `
key: dangerous-ops
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        title: Git Push Approval
        viewportType: builtin
        viewportKey: confirm_dialog
      - match: push --force
        level: 5
        viewportType: html
        viewportKey: git_force_push
`
	if err := os.WriteFile(filepath.Join(root, "dangerous.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].FileKey != "dangerous-ops" {
		t.Fatalf("unexpected file key: %#v", rules[0])
	}
	if rules[0].Title != "Git Push Approval" {
		t.Fatalf("expected title to be flattened, got %#v", rules[0])
	}
	if rules[1].Match != "push --force" || len(rules[1].MatchTokens) != 2 {
		t.Fatalf("unexpected flattened rule: %#v", rules[1])
	}
}

func TestLoadRulesSkipsDisabledFiles(t *testing.T) {
	root := t.TempDir()
	content := `
enabled: false
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        viewportType: builtin
        viewportKey: confirm_dialog
`
	if err := os.WriteFile(filepath.Join(root, "disabled.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("expected no rules, got %#v", rules)
	}
}

func TestLoadRulesRejectsInvalidRule(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 1
        viewportType: invalid
        viewportKey: confirm_dialog
`
	if err := os.WriteFile(filepath.Join(root, "invalid.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	if _, err := loadRulesFromDir(root); err == nil {
		t.Fatalf("expected invalid rule error")
	}
}

func TestLoadRulesDeduplicatesFirstMatch(t *testing.T) {
	root := t.TempDir()
	first := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        viewportType: builtin
        viewportKey: confirm_dialog
`
	second := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 5
        viewportType: html
        viewportKey: another
`
	if err := os.WriteFile(filepath.Join(root, "a.yml"), []byte(first), 0o644); err != nil {
		t.Fatalf("write first rule file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.yml"), []byte(second), 0o644); err != nil {
		t.Fatalf("write second rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected deduplicated rules, got %#v", rules)
	}
	if rules[0].Level != 2 {
		t.Fatalf("expected first rule to win, got %#v", rules[0])
	}
}

func TestLoadRulesRejectsViewportTypeConflict(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        viewportType: builtin
        viewportKey: same
  - command: docker
    subcommands:
      - match: rm
        level: 3
        viewportType: html
        viewportKey: same
`
	if err := os.WriteFile(filepath.Join(root, "conflict.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write conflict rule file: %v", err)
	}

	if _, err := loadRulesFromDir(root); err == nil {
		t.Fatalf("expected viewport conflict error")
	}
}

func TestLoadRulesSupportsLegacyToolTypeField(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        toolType: builtin
        viewportKey: confirm_dialog
`
	if err := os.WriteFile(filepath.Join(root, "legacy.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write legacy rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %#v", rules)
	}
	if rules[0].ViewportType != "builtin" {
		t.Fatalf("expected legacy toolType to map to viewportType, got %#v", rules[0])
	}
}

func TestLoadRulesDefaultsViewportToBuiltinConfirmDialog(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: reboot
    subcommands:
      - match: now
        level: 1
`
	if err := os.WriteFile(filepath.Join(root, "defaults.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %#v", rules)
	}
	if rules[0].ViewportType != "builtin" || rules[0].ViewportKey != "confirm_dialog" {
		t.Fatalf("expected default builtin confirm dialog, got %#v", rules[0])
	}
}

func TestLoadRulesAllowsEmptyMatch(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: reboot
    subcommands:
      - match: ""
        level: 1
`
	if err := os.WriteFile(filepath.Join(root, "empty-match.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %#v", rules)
	}
	if len(rules[0].MatchTokens) != 0 {
		t.Fatalf("expected empty match tokens, got %#v", rules[0].MatchTokens)
	}
}

func TestLoadRulesSupportsInlineMapSubcommands(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: curl
    subcommands:
      - { match: "| bash", level: 1 }
`
	if err := os.WriteFile(filepath.Join(root, "inline.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %#v", rules)
	}
	if rules[0].Match != "| bash" {
		t.Fatalf("expected inline map match, got %#v", rules[0])
	}
	if rules[0].Level != 1 {
		t.Fatalf("expected inline map level 1, got %#v", rules[0])
	}
}

func TestLoadRulesPassThroughFlags(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: mock
    passThroughFlags:
      - --help
      - " -h "
      - --version
      - --HELP
      - 1
    subcommands:
      - match: create-leave
        level: 1
      - match: create-expense
        level: 2
`
	if err := os.WriteFile(filepath.Join(root, "mock.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %#v", rules)
	}

	want := []string{"--help", "-h", "--version"}
	for _, rule := range rules {
		if !reflect.DeepEqual(rule.PassThroughFlags, want) {
			t.Fatalf("expected normalized pass-through flags %v, got %#v", want, rule.PassThroughFlags)
		}
	}
}

func TestLoadRulesSupportsFlowStylePassThroughFlags(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: mock
    passThroughFlags: [--help, " -h ", --version]
    subcommands:
      - match: create-leave
        level: 1
`
	if err := os.WriteFile(filepath.Join(root, "mock.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	rules, err := loadRulesFromDir(root)
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %#v", rules)
	}
	want := []string{"--help", "-h", "--version"}
	if !reflect.DeepEqual(rules[0].PassThroughFlags, want) {
		t.Fatalf("expected flow-style pass-through flags %v, got %#v", want, rules[0].PassThroughFlags)
	}
}
