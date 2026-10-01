package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillRuntimeEnvPathAppendsInsteadOfReplacing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool-skill")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: tool-skill\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := `{"PATH":"bin` + string(os.PathListSeparator) + `/usr/bin","TOOL_MODE":"fast"}`
	if err := os.WriteFile(filepath.Join(dir, ".runtime-env.json"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	skill, found, err := loadSkillDefinitionFromDir(dir, "tool-skill", 0)
	if err != nil || !found {
		t.Fatalf("a skill declaring PATH must load: %v", err)
	}
	if _, ok := skill.RuntimeEnv["PATH"]; ok || skill.RuntimeEnv["TOOL_MODE"] != "fast" {
		t.Fatalf("PATH must not be an override: %#v", skill.RuntimeEnv)
	}
	if len(skill.PathAppend) != 2 || skill.PathAppend[0] != filepath.Join(dir, "bin") || !strings.HasSuffix(skill.PathAppend[1], "usr"+string(filepath.Separator)+"bin") {
		t.Fatalf("relative entries resolve against the skill: %#v", skill.PathAppend)
	}
	if err := os.WriteFile(filepath.Join(dir, ".runtime-env.json"), []byte(`{"LD_PRELOAD":"x.so"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSkillDefinitionFromDir(dir, "tool-skill", 0); err == nil {
		t.Fatal("loader injection variables stay forbidden")
	}
}
