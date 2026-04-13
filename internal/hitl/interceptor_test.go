package hitl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryCheck(t *testing.T) {
	root := t.TempDir()
	content := `
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
	if err := os.WriteFile(filepath.Join(root, "rules.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	if result := registry.Check("git status", 0); result.Intercepted {
		t.Fatalf("did not expect git status to be intercepted: %#v", result)
	}

	if result := registry.Check("git push origin main", 2); result.Intercepted {
		t.Fatalf("expected chat level 2 to bypass push rule: %#v", result)
	}

	result := registry.Check("git push origin main", 0)
	if !result.Intercepted {
		t.Fatalf("expected git push to be intercepted")
	}
	if result.Rule.Match != "push" {
		t.Fatalf("unexpected matched rule: %#v", result.Rule)
	}

	force := registry.Check("git push --force origin main", 0)
	if !force.Intercepted {
		t.Fatalf("expected force push to be intercepted")
	}
	if force.Rule.Match != "push --force" {
		t.Fatalf("expected more specific rule to win, got %#v", force.Rule)
	}
}

func TestRegistryToolLookup(t *testing.T) {
	root := t.TempDir()
	content := `
commands:
  - command: git
    subcommands:
      - match: push
        level: 2
        hitlType: system
        toolType: builtin
        viewportKey: confirm_dialog
`
	if err := os.WriteFile(filepath.Join(root, "rules.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rule file: %v", err)
	}

	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	tool, ok := registry.Tool("_hitl_confirm_dialog_")
	if !ok {
		t.Fatalf("expected synthetic tool definition")
	}
	if tool.Meta["toolType"] != "builtin" || tool.Meta["viewportKey"] != "confirm_dialog" {
		t.Fatalf("unexpected synthetic tool meta: %#v", tool.Meta)
	}
}
