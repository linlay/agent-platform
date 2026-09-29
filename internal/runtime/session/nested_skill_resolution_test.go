package session

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
)

type nestedSkillCatalog map[string]catalog.SkillDefinition

func (c nestedSkillCatalog) SkillKeys() []string {
	keys := []string{}
	for key := range c {
		keys = append(keys, key)
	}
	return keys
}
func (c nestedSkillCatalog) SkillDefinition(key string) (catalog.SkillDefinition, bool) {
	def, ok := c[key]
	return def, ok
}

func TestMustUsePackageMembersKeepExactKeysAndIndependentRoots(t *testing.T) {
	root := t.TempDir()
	center := nestedSkillCatalog{}
	for _, key := range []string{"demo", "suite/demo"} {
		dir := filepath.Join(root, filepath.FromSlash(key))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\ndescription: "+key+"\n---\nUse "+key), 0600); err != nil {
			t.Fatal(err)
		}
		center[key] = catalog.SkillDefinition{Key: key}
	}
	if err := os.WriteFile(filepath.Join(root, "suite", "package.json"), []byte(`{"name":"suite","skills":[{"key":"demo"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ResolveMustUseSkills(catalog.AgentDefinition{}, root, center, []string{"demo", "suite/demo", "suite/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 2 || result.Skills[0].RootPath == result.Skills[1].RootPath {
		t.Fatalf("collapsed names: %+v", result)
	}
	if result.Skills[0].Definition.Description != "demo" || result.Skills[1].Definition.Description != "suite/demo" {
		t.Fatalf("wrong source %+v", result)
	}
	if result.Skills[1].InstructionsPath != "@skills-center/suite/demo/SKILL.md" {
		t.Fatal(result.Skills[1].InstructionsPath)
	}
	access, err := MustUseSkillRunAccess(result.Skills)
	if err != nil {
		t.Fatal(err)
	}
	_ = access
	// Removing the independent skill must not fall back to the package member.
	delete(center, "demo")
	if _, err := ResolveMustUseSkills(catalog.AgentDefinition{}, root, center, []string{"demo"}); err == nil {
		t.Fatal("short-name alias resolved package member")
	}
	for _, key := range []string{"suite/../demo", "suite/demo/deeper", "suite//demo"} {
		if _, err := ResolveMustUseSkillRoot(root, key); err == nil {
			t.Errorf("unsafe root accepted %q", key)
		}
	}
}

func TestConfiguredPackageMemberUsesRuntimeCopy(t *testing.T) {
	runtime := t.TempDir()
	dir := filepath.Join(runtime, "skills", "suite", "demo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\ndescription: runtime\n---\nRuntime body"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ResolveMustUseSkills(catalog.AgentDefinition{RuntimeDir: runtime, Skills: []string{"suite/demo"}}, "", nil, []string{"suite/demo"})
	if err != nil || len(result.Skills) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if result.Skills[0].Extra || result.Skills[0].InstructionsPath != "@skills/suite/demo/SKILL.md" {
		t.Fatalf("wrong runtime resolution %+v", result)
	}
}
