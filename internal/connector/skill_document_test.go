package connector

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillDocumentFixture(t *testing.T, id, layout string) Sources {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, layout), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"connector.json":                  `{"id":"` + id + `","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`,
		"cli.json":                        `{}`,
		filepath.Join(layout, "SKILL.md"): "---\nname: demo\ndescription: Sample skill\n---\n# Guide\nRead this guide.\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return Sources{ExternalRoot: root, BuiltinRoot: root}
}

func TestConnectorSkillDocumentsReadBothSourcesAndLayouts(t *testing.T) {
	for _, id := range []string{"external", "builtin.demo"} {
		for _, layout := range []string{"skills", "skills/demo"} {
			t.Run(id+"/"+layout, func(t *testing.T) {
				sources := skillDocumentFixture(t, id, layout)
				list, err := sources.SkillDocuments(id)
				if err != nil || len(list) != 1 {
					t.Fatalf("list=%v err=%v", list, err)
				}
				detail, err := sources.ReadSkillDocument(id, "demo")
				if err != nil {
					t.Fatal(err)
				}
				if detail.Path != layout+"/SKILL.md" || len(detail.SHA256) != 64 || detail.Size != int64(len(detail.Content)) || detail.UpdatedAt == 0 {
					t.Fatalf("detail=%+v", detail)
				}
				if strings.Contains(detail.Path, sources.ExternalRoot) {
					t.Fatal("absolute path leaked")
				}
				if _, err := sources.ReadSkillDocument(id, "missing"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing: %v", err)
				}
			})
		}
	}
}

func TestConnectorSkillDocumentsRejectEscapeAndOversize(t *testing.T) {
	sources := skillDocumentFixture(t, "demo", "skills/demo")
	for _, target := range [][2]string{{"../demo", "demo"}, {"demo", "../demo"}, {"demo", ""}} {
		if _, err := sources.ReadSkillDocument(target[0], target[1]); !errors.Is(err, ErrInvalidSkillTarget) {
			t.Fatalf("target=%v err=%v", target, err)
		}
	}
	file := filepath.Join(sources.ExternalRoot, "demo", "skills", "demo", "SKILL.md")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(original, []byte(strings.Repeat("x", MaxSkillDocumentBytes))...), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := sources.ReadSkillDocument("demo", "demo"); !errors.Is(err, ErrSkillDocumentTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(outside, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := sources.ReadSkillDocument("demo", "demo"); err == nil {
		t.Fatal("outside symlink accepted")
	}
}
