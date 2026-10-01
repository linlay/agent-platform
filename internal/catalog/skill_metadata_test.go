package catalog

import (
	"agent-platform/internal/skillmeta"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillMetadataVersionAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stable-id")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	prompt := "---\nname: stable-id\ndescription: Original\nversion: \"2.0\"\nmetadata:\n  version: \"1.0\"\n  revision: r18\n  displayName: 流程助手\n  i18n:\n    en:\n      displayName: Workflow\n    zh-CN: broken\n---\nBody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(prompt), 0644); err != nil {
		t.Fatal(err)
	}
	skill, _, err := loadSkillDefinitionFromDir(dir, "stable-id", 0)
	if err != nil {
		t.Fatal(err)
	}
	if skill.ID != "stable-id" || skill.Name != "stable-id" || skill.Version != "2.0" || skill.Prompt != prompt {
		t.Fatal(skill)
	}
	meta := skillSummaryMeta(skill)["metadata"].(map[string]any)
	if meta["version"] != "2.0" {
		t.Fatal(meta)
	}
	item, err := buildAdminSkill(root, "stable-id", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != AdminSkillStatusReady || len(item.Diagnostics) != 2 {
		t.Fatalf("%#v", item)
	}
	p, _ := item.Presentation.Resolve("en-US", item.Name, item.ID, item.Description)
	if p.DisplayName != "Workflow" || p.Revision != "r18" {
		t.Fatal(p)
	}
	for _, tt := range []struct{ input, want string }{
		{"metadata:\n  version: \"1.0\"", "1.0"}, {"metadata:\n  revision: r18", ""}, {"version: \"\"\nmetadata:\n  version: \"3.0\"", "3.0"},
	} {
		_, _, _, _, version := parseSkillPromptMetadata("---\nname: test\n" + tt.input + "\n---\nBody")
		if version != tt.want {
			t.Fatalf("%s: %q", tt.input, version)
		}
	}
}

func TestSkillPackageVersionDiagnosticsDoNotRewrite(t *testing.T) {
	root := t.TempDir()
	for key, content := range map[string]string{"missing": "---\nname: missing\ndescription: Test\n---\nBody", "different": "---\nname: different\nversion: \"2\"\n---\nBody"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	original := SkillPackageRecord{Skills: []SkillPackageRecordSkill{{ID: "missing", Version: "1"}, {ID: "different", Version: "1"}}}
	got := skillPackageVersionDiagnostics(root, original)
	for i, want := range []string{"missing_skill_version", "skill_package_version_mismatch"} {
		if len(got.Skills[i].Diagnostics) != 1 || got.Skills[i].Diagnostics[0].Code != want || len(original.Skills[i].Diagnostics) != 0 || got.Skills[i].Version != "1" {
			t.Fatalf("%#v", got)
		}
	}
}

func TestSkillNameKeyMismatchIsOnlyWarning(t *testing.T) {
	for _, name := range []string{"stable-id", "Friendly Name"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "stable-id")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			prompt := "---\nname: " + name + "\ndescription: Description\n---\nBody"
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(prompt), 0644); err != nil {
				t.Fatal(err)
			}
			def, found, err := loadSkillDefinitionFromDir(dir, "stable-id", 0)
			if err != nil || !found || def.ID != "stable-id" || def.Name != name {
				t.Fatalf("load: %#v %v", def, err)
			}
			item, err := buildAdminSkill(root, "stable-id", nil, false)
			if err != nil || item.Status != AdminSkillStatusReady {
				t.Fatalf("admin: %#v %v", item, err)
			}
			diagnostics := ValidateSkillCandidate("stable-id", []byte(prompt), 0)
			want := 0
			if name != "stable-id" {
				want = 1
			}
			if len(diagnostics) != want || len(item.Diagnostics) != want {
				t.Fatalf("diagnostics: %#v %#v", diagnostics, item.Diagnostics)
			}
			if want == 1 && (diagnostics[0].Severity != "warning" || diagnostics[0].Code != "skill_name_id_mismatch" || item.Diagnostics[0].Code != diagnostics[0].Code) {
				t.Fatal(diagnostics)
			}
		})
	}
}

func TestSkillTopLevelDisplayName(t *testing.T) {
	for _, tt := range []struct {
		fields, want string
		warnings     int
	}{
		{"displayName: 顶层名称", "顶层名称", 0},
		{"metadata:\n  displayName: 兼容名称", "兼容名称", 0},
		{"displayName: 顶层名称\nmetadata:\n  displayName: 兼容名称", "顶层名称", 1},
		{"displayName: 同名\nmetadata:\n  displayName: 同名", "同名", 0},
		{"displayName: \"  \"\nmetadata:\n  displayName: 兼容名称", "兼容名称", 0},
		{"displayName: 顶层名称\nmetadata:", "顶层名称", 0},
		{"metadata:\n  revision: r1", "sample", 0},
	} {
		prompt := "---\nname: sample\ndescription: Test\n" + tt.fields + "\n---\nBody"
		name, description, _, metadata, version := parseSkillPromptMetadata(prompt)
		p, _ := skillmeta.Parse(metadata, version).Resolve("en-US", name, "sample", description)
		if p.DisplayName != tt.want {
			t.Fatalf("%s: %#v", tt.fields, p)
		}
		if diagnostics := skillMetadataDiagnostics("sample", prompt); len(diagnostics) != tt.warnings {
			t.Fatalf("%s: %#v", tt.fields, diagnostics)
		}
	}
	prompt := "---\nname: sample\ndescription: Test\ndisplayName: 默认名称\nmetadata:\n  i18n:\n    en:\n      displayName: English name\n---\nBody"
	name, description, _, metadata, version := parseSkillPromptMetadata(prompt)
	p, _ := skillmeta.Parse(metadata, version).Resolve("en-US", name, "sample", description)
	if p.DisplayName != "English name" {
		t.Fatal(p)
	}
}

// Set SKILL_FORMAT_EXAMPLE_DIR to validate the separately maintained deployment
// example with the same parser, loader and diagnostics used by Platform.
func TestSkillFormatExample(t *testing.T) {
	dir := os.Getenv("SKILL_FORMAT_EXAMPLE_DIR")
	if dir == "" {
		t.Skip("SKILL_FORMAT_EXAMPLE_DIR is not set")
	}
	key := filepath.Base(dir)
	content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := ValidateSkillCandidate(key, content, 0); len(diagnostics) != 0 {
		t.Fatalf("example diagnostics: %#v", diagnostics)
	}
	def, found, err := loadSkillDefinitionFromDir(dir, key, 0)
	if err != nil || !found {
		t.Fatalf("load example: %v", err)
	}
	if def.ID != "skill-format-example" || def.Name != def.ID || def.Version != "1.0.0" || len(def.Triggers) != 3 {
		t.Fatalf("example: %#v", def)
	}
	for locale, want := range map[string]string{"zh-CN": "技能格式示例", "en-US": "Skill Format Example", "fr": "技能格式示例"} {
		p, description := skillmeta.Parse(def.Metadata, def.Version).Resolve(locale, def.Name, def.ID, def.Description)
		if p.DisplayName != want || p.Revision != "2026-09-26-r1" || description == "" {
			t.Fatalf("%s: %#v", locale, p)
		}
	}
}
