package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/config"
)

func TestEditableSkillAdminScansInvalidRuntimeEnvUsageAndSymlink(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "demo-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: Demo Skill\ndescription: Demo description\ntriggers:\n  - demo\nmetadata:\n  version: 1.0.0\n---\n\nBody\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, ".runtime-env.json"), []byte(`{"PORT":3000}`), 0o644); err != nil {
		t.Fatalf("write runtime env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "references", "guide.md"), []byte("guide"), 0o644); err != nil {
		t.Fatalf("write guide: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(skillDir, "references", "link")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	registry := &FileRegistry{
		cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}},
		adminAgents: map[string]AdminAgent{
			"agent-a": {Key: "agent-a", Skills: []string{"demo-skill"}},
		},
	}

	items, err := registry.AdminSkills()
	if err != nil {
		t.Fatalf("AdminSkills: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one skill, got %#v", items)
	}
	item := items[0]
	if item.Name != "Demo Skill" || item.Description != "Demo description" {
		t.Fatalf("unexpected metadata: %#v", item)
	}
	if item.Status != AdminSkillStatusInvalid {
		t.Fatalf("expected invalid status from runtime env diagnostic, got %#v", item)
	}
	if len(item.UsedByAgents) != 1 || item.UsedByAgents[0] != "agent-a" {
		t.Fatalf("unexpected usage: %#v", item.UsedByAgents)
	}
	if !hasCatalogDiagnostic(item.Diagnostics, "invalid_runtime_env") {
		t.Fatalf("expected invalid_runtime_env diagnostic, got %#v", item.Diagnostics)
	}
	if runtime.GOOS != "windows" && !hasCatalogDiagnostic(item.Diagnostics, "symlink_skipped") {
		t.Fatalf("expected symlink_skipped diagnostic, got %#v", item.Diagnostics)
	}
}

func TestEditableSkillPathGuardsAndBinaryRead(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
	if _, err := registry.CreateEditableSkill("../bad", "# Bad\n", nil); !errors.Is(err, ErrInvalidSkillKey) {
		t.Fatalf("expected invalid key, got %v", err)
	}
	if _, err := registry.CreateEditableSkill("demo", "# Demo\n", []EditableSkillInlineFile{{Path: `refs\bad.md`, Content: "x"}}); !errors.Is(err, ErrInvalidSkillPath) {
		t.Fatalf("expected invalid path, got %v", err)
	}
	if _, err := registry.CreateEditableSkill("demo", "# Demo\n", nil); err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if _, err := registry.WriteEditableSkillFile("demo", "../bad.md", "x", "", ""); !errors.Is(err, ErrInvalidSkillPath) {
		t.Fatalf("expected invalid write path, got %v", err)
	}
	if _, err := registry.WriteEditableSkillFile("demo", ".runtime-env.json", `{"PORT":3000}`, "", ""); err == nil {
		t.Fatal("expected invalid runtime env write error")
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if _, err := registry.ReadEditableSkillFile("demo", "blob.bin"); !errors.Is(err, ErrSkillFileBinary) {
		t.Fatalf("expected binary read rejection, got %v", err)
	}
}

func TestAdminSkillIconRequiresRegularFile(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "demo-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}

	item, found, err := registry.AdminSkill("demo-skill")
	if err != nil || !found || item.IconPath != "" {
		t.Fatalf("missing icon = %#v, found=%v, err=%v", item, found, err)
	}

	iconPath := filepath.Join(skillDir, "assets", "demo-skill.png")
	if err := os.Mkdir(iconPath, 0o755); err != nil {
		t.Fatalf("mkdir icon candidate: %v", err)
	}
	item, found, err = registry.AdminSkill("demo-skill")
	if err != nil || !found || item.IconPath != "" {
		t.Fatalf("directory icon = %#v, found=%v, err=%v", item, found, err)
	}
	if err := os.Remove(iconPath); err != nil {
		t.Fatalf("remove icon directory: %v", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "outside.png"), iconPath); err != nil {
			t.Fatalf("symlink icon: %v", err)
		}
		item, found, err = registry.AdminSkill("demo-skill")
		if err != nil || !found || item.IconPath != "" {
			t.Fatalf("symlink icon = %#v, found=%v, err=%v", item, found, err)
		}
		if err := os.Remove(iconPath); err != nil {
			t.Fatalf("remove icon symlink: %v", err)
		}
	}

	if err := os.WriteFile(iconPath, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatalf("write icon: %v", err)
	}
	item, found, err = registry.AdminSkill("demo-skill")
	if err != nil || !found || item.IconPath != "assets/demo-skill.png" {
		t.Fatalf("regular icon = %#v, found=%v, err=%v", item, found, err)
	}
}

func hasCatalogDiagnostic(items []AdminSkillDiagnostic, code string) bool {
	for _, item := range items {
		if item.Code == code {
			return true
		}
	}
	return false
}
