package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
)

func TestResolveRequiredSkillKeysRequiresConfiguredResolvableSkill(t *testing.T) {
	marketDir := t.TempDir()
	runtimeDir := t.TempDir()
	skillDir := filepath.Join(runtimeDir, "skills", "design")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Design\n\nFollow the design workflow."), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	def := catalog.AgentDefinition{
		Key:        "coder",
		RuntimeDir: runtimeDir,
		Skills:     []string{"design"},
	}

	got, err := resolveRequiredSkillKeys(def, marketDir, []string{" design ", "DESIGN"})
	if err != nil {
		t.Fatalf("resolve required skill: %v", err)
	}
	if len(got) != 1 || got[0] != "design" {
		t.Fatalf("unexpected required skills %#v", got)
	}
	if _, err := resolveRequiredSkillKeys(def, marketDir, []string{"missing"}); err == nil {
		t.Fatal("expected unconfigured required skill to fail")
	}
}

func TestBuildRequiredSkillConstraintIsMandatory(t *testing.T) {
	got := buildRequiredSkillConstraint([]string{"design"})
	for _, expected := range []string{
		"Required skills for this run:",
		"- design",
		"must load the complete SKILL.md",
		"must not be silently ignored",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in constraint %q", expected, got)
		}
	}
}
