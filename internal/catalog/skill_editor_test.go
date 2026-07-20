package catalog

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
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

func TestWriteEditableSkillArchiveIncludesSafeFilesOnly(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "demo-skill")
	for _, dir := range []string{"assets", "references", "scripts", ".bash-hooks"} {
		if err := os.MkdirAll(filepath.Join(skillDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	files := map[string][]byte{
		"SKILL.md":              []byte("# Demo\n"),
		"assets/logo.png":       []byte{0x89, 'P', 'N', 'G'},
		"references/guide.md":   []byte("guide\n"),
		"scripts/run.sh":        []byte("#!/bin/sh\necho demo\n"),
		".bash-hooks/pre-start": []byte("echo hook\n"),
		".runtime-env.json":     []byte(`{"TOKEN":"secret"}`),
	}
	for relPath, content := range files {
		if err := os.WriteFile(filepath.Join(skillDir, filepath.FromSlash(relPath)), content, 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}
	if err := os.Chmod(filepath.Join(skillDir, "scripts", "run.sh"), 0o755); err != nil {
		t.Fatalf("chmod script: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(skillDir, "assets", "outside-link")); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
	}

	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
	var output bytes.Buffer
	if err := registry.WriteEditableSkillArchive("demo-skill", &output); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	entries := map[string]*zip.File{}
	for _, entry := range reader.File {
		entries[entry.Name] = entry
	}
	for _, name := range []string{"SKILL.md", "assets/logo.png", "references/guide.md", "scripts/run.sh", ".bash-hooks/pre-start"} {
		if entries[name] == nil {
			t.Fatalf("archive missing %s: %#v", name, entries)
		}
	}
	if entries[".runtime-env.json"] != nil || entries["assets/outside-link"] != nil {
		t.Fatalf("archive contains excluded files: %#v", entries)
	}
	if entries["scripts/run.sh"].Mode()&0o111 == 0 {
		t.Fatalf("archive did not preserve executable script mode: %v", entries["scripts/run.sh"].Mode())
	}
	content, err := entries["SKILL.md"].Open()
	if err != nil {
		t.Fatalf("open archived skill: %v", err)
	}
	defer content.Close()
	data, err := io.ReadAll(content)
	if err != nil || string(data) != "# Demo\n" {
		t.Fatalf("unexpected archived skill content %q, err=%v", string(data), err)
	}
}

func TestWriteEditableSkillArchiveRejectsOversizedContent(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	oversized := filepath.Join(skillDir, "assets.bin")
	if err := os.WriteFile(oversized, nil, 0o644); err != nil {
		t.Fatalf("create oversized file: %v", err)
	}
	if err := os.Truncate(oversized, EditableSkillMaxArchiveBytes+1); err != nil {
		t.Fatalf("create sparse oversized file: %v", err)
	}
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
	if err := registry.WriteEditableSkillArchive("demo-skill", io.Discard); !errors.Is(err, ErrSkillArchiveTooLarge) {
		t.Fatalf("expected archive size rejection, got %v", err)
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
