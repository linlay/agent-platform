package session

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
)

func TestResolveSkillPathAppendFiltersByOwnershipAndAdminRoots(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "ru-agents", "agent", "revision")
	skillDir := filepath.Join(runtimeDir, "skills", "tool")
	own := filepath.Join(skillDir, "bin")
	admin := filepath.Join(t.TempDir(), "approved", "bin")
	foreign := t.TempDir()
	for _, dir := range []string{own, admin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: tool\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := `{"PATH":"bin` + string(os.PathListSeparator) + admin + string(os.PathListSeparator) + foreign + `"}`
	if err := os.WriteFile(filepath.Join(skillDir, ".runtime-env.json"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	installRuntimeSkillFixture(t, runtimeDir, "tool")
	def := catalog.AgentDefinition{Key: "a", RuntimeDir: runtimeDir, Skills: []string{"tool"}}
	got := ResolveSkillPathAppend(def, []string{"tool", "tool"}, []string{filepath.Dir(admin)})
	if len(got) != 2 {
		t.Fatalf("want own and admin-approved dirs only, got %v", got)
	}
	for _, dir := range got {
		if dir == foreign {
			t.Fatalf("directory outside the skill and admin roots was accepted: %v", got)
		}
	}
}
