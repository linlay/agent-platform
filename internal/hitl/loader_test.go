package hitl

import (
	"os"
	"path/filepath"
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
        hitlType: system
        toolType: builtin
        viewportKey: confirm_dialog
      - match: push --force
        level: 5
        hitlType: business
        toolType: html
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
        hitlType: system
        toolType: builtin
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
        level: 0
        hitlType: invalid
        toolType: builtin
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
        hitlType: system
        toolType: builtin
        viewportKey: confirm_dialog
`
	second := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 5
        hitlType: business
        toolType: html
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

func TestLoadRulesRejectsViewportToolTypeConflict(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        hitlType: system
        toolType: builtin
        viewportKey: same
  - command: docker
    subcommands:
      - match: rm
        level: 3
        hitlType: business
        toolType: html
        viewportKey: same
`
	if err := os.WriteFile(filepath.Join(root, "conflict.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write conflict rule file: %v", err)
	}

	if _, err := loadRulesFromDir(root); err == nil {
		t.Fatalf("expected viewport conflict error")
	}
}
