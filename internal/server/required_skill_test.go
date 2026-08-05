package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

type testSkillCenter map[string]catalog.SkillDefinition

func (m testSkillCenter) Skills(_ string) []api.SkillSummary {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]api.SkillSummary, 0, len(keys))
	for _, key := range keys {
		items = append(items, api.SkillSummary{Key: key})
	}
	return items
}

func (m testSkillCenter) SkillDefinition(key string) (catalog.SkillDefinition, bool) {
	definition, ok := m[key]
	return definition, ok
}

func writeTestSkill(t *testing.T, root string, key string) {
	t.Helper()
	skillDir := filepath.Join(root, key)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+key+"\n\nFollow the workflow."), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func TestResolveMustUseSkillsSupportsConfiguredAndCenterSkills(t *testing.T) {
	centerDir := t.TempDir()
	runtimeDir := t.TempDir()
	writeTestSkill(t, filepath.Join(runtimeDir, "skills"), "design")
	writeTestSkill(t, centerDir, "pdf")
	def := catalog.AgentDefinition{
		Key:        "coder",
		RuntimeDir: runtimeDir,
		Skills:     []string{"design"},
	}
	center := testSkillCenter{
		"pdf": {Key: "pdf", Name: "PDF"},
	}

	got, err := resolveMustUseSkills(def, centerDir, center, []string{" design ", "DESIGN", " PDF ", "pdf", ""})
	if err != nil {
		t.Fatalf("resolve must-use skills: %v", err)
	}
	if strings.Join(got.Keys, ",") != "design,pdf" {
		t.Fatalf("unexpected must-use skills %#v", got.Keys)
	}
	if !got.HasExtraSkills || len(got.Skills) != 2 {
		t.Fatalf("unexpected resolution %#v", got)
	}
	if got.Skills[0].InstructionsPath != "@skills/design/SKILL.md" || got.Skills[0].Extra {
		t.Fatalf("configured skill = %#v", got.Skills[0])
	}
	if got.Skills[1].InstructionsPath != "@skills-center/pdf/SKILL.md" || !got.Skills[1].Extra {
		t.Fatalf("center skill = %#v", got.Skills[1])
	}
	if _, err := resolveMustUseSkills(def, centerDir, center, []string{"missing"}); err == nil {
		t.Fatal("expected missing must-use skill to fail")
	}
}

func TestResolveMustUseSkillsRevalidatesCenterContent(t *testing.T) {
	centerDir := t.TempDir()
	center := testSkillCenter{"pdf": {Key: "pdf"}}
	if _, err := resolveMustUseSkills(catalog.AgentDefinition{Key: "coder"}, centerDir, center, []string{"pdf"}); err == nil {
		t.Fatal("expected catalog entry without current SKILL.md to fail")
	}
}

func TestBuildMustUseSkillConstraintIsMandatory(t *testing.T) {
	got := buildMustUseSkillConstraint([]resolvedMustUseSkill{{
		Key:              "design",
		InstructionsPath: "@skills/design/SKILL.md",
	}})
	for _, expected := range []string{
		"Must-use skills for this run:",
		"skillId: design",
		"instructionsPath: @skills/design/SKILL.md",
		"must read the complete SKILL.md",
		"None may be skipped",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in constraint %q", expected, got)
		}
	}
}

func TestRuntimeExtraMountsForMustUseSkillsAddsOneReadonlyCenterMount(t *testing.T) {
	mounts := runtimeExtraMountsForMustUseSkills([]map[string]any{
		{"platform": "teams", "mode": "ro"},
	}, true)
	if len(mounts) != 2 || mounts[1] != (contracts.SandboxExtraMount{Platform: "skills-center", Mode: "ro"}) {
		t.Fatalf("mounts = %#v", mounts)
	}

	mounts = runtimeExtraMountsForMustUseSkills([]map[string]any{
		{"platform": "skills-center", "mode": "rw"},
	}, true)
	if len(mounts) != 1 || mounts[0].Mode != "ro" {
		t.Fatalf("deduplicated mounts = %#v", mounts)
	}
}
