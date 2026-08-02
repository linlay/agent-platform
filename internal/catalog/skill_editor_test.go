package catalog

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	if _, err := registry.CreateEditableSkill("hidden.example", "# Hidden\n", nil); !errors.Is(err, ErrInvalidSkillKey) {
		t.Fatalf("expected example key rejection, got %v", err)
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

func TestImportEditableSkillArchiveSupportsRootAndWrappedLayouts(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}

	rootArchive := buildSkillImportZIP(t, []skillImportZIPEntry{
		{name: "SKILL.md", content: []byte("---\nname: Root Skill\ndescription: Imported\n---\n\nUse it.\n"), mode: 0o644},
		{name: "scripts/run.sh", content: []byte("#!/bin/sh\necho ok\n"), mode: 0o755},
		{name: ".runtime-env.json", content: []byte(`{"MODE":"safe"}`), mode: 0o644},
		{name: "__MACOSX/._SKILL.md", content: []byte("noise"), mode: 0o644},
		{name: "assets/.DS_Store", content: []byte("noise"), mode: 0o644},
	})
	item, err := registry.ImportEditableSkillArchive("root-skill", bytes.NewReader(rootArchive), int64(len(rootArchive)))
	if err != nil {
		t.Fatalf("import root archive: %v", err)
	}
	if item.Key != "root-skill" || item.Name != "Root Skill" || item.Status != AdminSkillStatusReady {
		t.Fatalf("unexpected imported skill: %#v", item)
	}
	scriptInfo, err := os.Stat(filepath.Join(root, "root-skill", "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("stat imported script: %v", err)
	}
	if scriptInfo.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected executable mode, got %v", scriptInfo.Mode())
	}
	if _, err := os.Stat(filepath.Join(root, "root-skill", "__MACOSX")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected metadata directory to be ignored, got %v", err)
	}

	wrappedArchive := buildSkillImportZIP(t, []skillImportZIPEntry{
		{name: "wrapped/", dir: true, mode: 0o755},
		{name: "wrapped/SKILL.md", content: []byte("# Wrapped Skill\n"), mode: 0o644},
		{name: "wrapped/references/guide.md", content: []byte("guide\n"), mode: 0o644},
	})
	wrapped, err := registry.ImportEditableSkillArchive("wrapped-skill", bytes.NewReader(wrappedArchive), int64(len(wrappedArchive)))
	if err != nil {
		t.Fatalf("import wrapped archive: %v", err)
	}
	if wrapped.Key != "wrapped-skill" || wrapped.Status != AdminSkillStatusReady {
		t.Fatalf("unexpected wrapped skill: %#v", wrapped)
	}
	if _, err := os.Stat(filepath.Join(root, "wrapped-skill", "wrapped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected wrapper directory to be stripped, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "wrapped-skill", "references", "guide.md")); err != nil {
		t.Fatalf("stat wrapped reference: %v", err)
	}
}

func TestDownloadedEditableSkillArchiveCanBeReimported(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
	if _, err := registry.CreateEditableSkill("source-skill", "# Source Skill\n", []EditableSkillInlineFile{
		{Path: "references/guide.md", Content: "guide\n"},
	}); err != nil {
		t.Fatalf("create source skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "source-skill", ".runtime-env.json"), []byte(`{"SECRET":"excluded"}`), 0o600); err != nil {
		t.Fatalf("write runtime env: %v", err)
	}
	var archive bytes.Buffer
	if err := registry.WriteEditableSkillArchive("source-skill", &archive); err != nil {
		t.Fatalf("download source skill: %v", err)
	}
	item, err := registry.ImportEditableSkillArchive("reimported-skill", bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatalf("reimport downloaded skill: %v", err)
	}
	if item.Key != "reimported-skill" || item.Status != AdminSkillStatusReady {
		t.Fatalf("unexpected reimported skill: %#v", item)
	}
	if _, err := os.Stat(filepath.Join(root, "reimported-skill", "references", "guide.md")); err != nil {
		t.Fatalf("stat reimported reference: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "reimported-skill", ".runtime-env.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime env should stay excluded from downloaded archive, got %v", err)
	}
}

func TestImportEditableSkillArchiveConcurrentDuplicateHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
	archive := buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("# Concurrent\n")}})

	start := make(chan struct{})
	errorsByImport := make([]error, 2)
	var wait sync.WaitGroup
	for i := range errorsByImport {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByImport[index] = registry.ImportEditableSkillArchive("concurrent-skill", bytes.NewReader(archive), int64(len(archive)))
		}(i)
	}
	close(start)
	wait.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errorsByImport {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSkillAlreadyExists):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent import error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected one success and one conflict, got successes=%d conflicts=%d", successes, conflicts)
	}
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read skills root: %v", err)
	}
	if len(rootEntries) != 1 || rootEntries[0].Name() != "concurrent-skill" {
		t.Fatalf("unexpected entries after concurrent import: %#v", rootEntries)
	}
}

func TestImportEditableSkillArchiveRejectsUnsafeOrInvalidContentWithoutResidue(t *testing.T) {
	tests := []struct {
		name    string
		entries []skillImportZIPEntry
		code    string
	}{
		{
			name:    "missing skill md",
			entries: []skillImportZIPEntry{{name: "README.md", content: []byte("readme")}},
			code:    "missing_skill_md",
		},
		{
			name:    "empty skill md",
			entries: []skillImportZIPEntry{{name: "SKILL.md", content: []byte(" \n")}},
			code:    "invalid_special_file",
		},
		{
			name: "invalid runtime env",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: ".runtime-env.json", content: []byte(`{"PORT":3000}`)},
			},
			code: "invalid_special_file",
		},
		{
			name: "zip slip",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: "../outside.txt", content: []byte("bad")},
			},
			code: "invalid_path",
		},
		{
			name: "absolute path",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: "/outside.txt", content: []byte("bad")},
			},
			code: "invalid_path",
		},
		{
			name: "backslash path",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: `scripts\run.sh`, content: []byte("bad")},
			},
			code: "invalid_path",
		},
		{
			name: "invalid utf8 skill md",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte{0xff, 0xfe}},
			},
			code: "invalid_special_file",
		},
		{
			name: "symlink",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: "scripts/link", content: []byte("../../outside"), mode: os.ModeSymlink | 0o777},
			},
			code: "symlink_not_allowed",
		},
		{
			name: "device file",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: "scripts/device", content: []byte("bad"), mode: os.ModeDevice | 0o600},
			},
			code: "unsupported_entry",
		},
		{
			name: "case collision",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: "assets/Logo.png", content: []byte("a")},
				{name: "assets/logo.png", content: []byte("b")},
			},
			code: "duplicate_path",
		},
		{
			name: "file directory conflict",
			entries: []skillImportZIPEntry{
				{name: "SKILL.md", content: []byte("# Demo\n")},
				{name: "assets", content: []byte("file")},
				{name: "assets/logo.png", content: []byte("image")},
			},
			code: "path_conflict",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
			archive := buildSkillImportZIP(t, tc.entries)
			_, err := registry.ImportEditableSkillArchive("demo-skill", bytes.NewReader(archive), int64(len(archive)))
			var validationErr *SkillArchiveValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("expected validation error, got %v", err)
			}
			if len(validationErr.Diagnostics) == 0 || validationErr.Diagnostics[0].Code != tc.code {
				t.Fatalf("expected diagnostic %q, got %#v", tc.code, validationErr.Diagnostics)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil {
				t.Fatalf("read skills root: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("expected no imported or staging directories, got %#v", entries)
			}
		})
	}
}

func TestImportEditableSkillArchiveRejectsInvalidDuplicateAndLimits(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{SkillsMarketDir: root}}}
	if _, err := registry.ImportEditableSkillArchive("invalid", bytes.NewReader([]byte("not a zip")), int64(len("not a zip"))); !errors.Is(err, ErrSkillArchiveInvalid) {
		t.Fatalf("expected invalid archive error, got %v", err)
	}
	archive := buildSkillImportZIP(t, []skillImportZIPEntry{{name: "SKILL.md", content: []byte("# Demo\n")}})
	if _, err := registry.ImportEditableSkillArchive("demo", bytes.NewReader(archive), int64(len(archive))); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := registry.ImportEditableSkillArchive("demo", bytes.NewReader(archive), int64(len(archive))); !errors.Is(err, ErrSkillAlreadyExists) {
		t.Fatalf("expected duplicate conflict, got %v", err)
	}
	if _, err := registry.ImportEditableSkillArchive("oversized", bytes.NewReader(nil), EditableSkillMaxUploadBytes+1); !errors.Is(err, ErrSkillArchiveUploadTooLarge) {
		t.Fatalf("expected upload size error, got %v", err)
	}

	entries := make([]skillImportZIPEntry, 0, EditableSkillMaxArchiveFiles+1)
	entries = append(entries, skillImportZIPEntry{name: "SKILL.md", content: []byte("# Demo\n")})
	for i := 0; i < EditableSkillMaxArchiveFiles; i++ {
		entries = append(entries, skillImportZIPEntry{name: fmt.Sprintf("references/%04d.md", i), content: []byte("x")})
	}
	tooMany := buildSkillImportZIP(t, entries)
	if _, err := registry.ImportEditableSkillArchive("too-many", bytes.NewReader(tooMany), int64(len(tooMany))); !errors.Is(err, ErrSkillArchiveTooManyFiles) {
		t.Fatalf("expected entry limit error, got %v", err)
	}

	oversizedFile := patchZIPDeclaredUncompressedSizes(t, buildSkillImportZIP(t, []skillImportZIPEntry{
		{name: "SKILL.md", content: []byte("# Demo\n")},
	}), map[string]uint32{"SKILL.md": uint32(EditableSkillMaxUploadBytes + 1)})
	if _, err := registry.ImportEditableSkillArchive("oversized-file", bytes.NewReader(oversizedFile), int64(len(oversizedFile))); !errors.Is(err, ErrSkillFileTooLarge) {
		t.Fatalf("expected per-file size error, got %v", err)
	}

	bombEntries := make([]skillImportZIPEntry, 0, 9)
	bombSizes := make(map[string]uint32, 9)
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("references/%02d.md", i)
		if i == 0 {
			name = "SKILL.md"
		}
		bombEntries = append(bombEntries, skillImportZIPEntry{name: name, content: []byte("x")})
		bombSizes[name] = uint32(EditableSkillMaxUploadBytes)
	}
	bomb := patchZIPDeclaredUncompressedSizes(t, buildSkillImportZIP(t, bombEntries), bombSizes)
	if _, err := registry.ImportEditableSkillArchive("bomb", bytes.NewReader(bomb), int64(len(bomb))); !errors.Is(err, ErrSkillArchiveTooLarge) {
		t.Fatalf("expected uncompressed archive size error, got %v", err)
	}
}

func TestCleanupEditableSkillImportStagingOnlyRemovesReservedEntries(t *testing.T) {
	root := t.TempDir()
	stagingDir := filepath.Join(root, editableSkillImportStagingPrefix+"old")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "real-skill"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := cleanupEditableSkillImportStaging(root); err != nil {
		t.Fatalf("cleanup staging: %v", err)
	}
	if _, err := os.Stat(stagingDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected staging removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "real-skill")); err != nil {
		t.Fatalf("expected real skill preserved: %v", err)
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

type skillImportZIPEntry struct {
	name    string
	content []byte
	mode    os.FileMode
	dir     bool
}

func buildSkillImportZIP(t *testing.T, entries []skillImportZIPEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		mode := entry.mode
		if entry.dir {
			if !strings.HasSuffix(header.Name, "/") {
				header.Name += "/"
			}
			mode |= os.ModeDir
		}
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP entry %s: %v", entry.name, err)
		}
		if !entry.dir {
			if _, err := file.Write(entry.content); err != nil {
				t.Fatalf("write ZIP entry %s: %v", entry.name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	return output.Bytes()
}

func patchZIPDeclaredUncompressedSizes(t *testing.T, archive []byte, sizes map[string]uint32) []byte {
	t.Helper()
	patched := append([]byte(nil), archive...)
	const centralHeaderSize = 46
	for offset := 0; offset+centralHeaderSize <= len(patched); {
		if binary.LittleEndian.Uint32(patched[offset:]) != 0x02014b50 {
			offset++
			continue
		}
		nameLength := int(binary.LittleEndian.Uint16(patched[offset+28:]))
		extraLength := int(binary.LittleEndian.Uint16(patched[offset+30:]))
		commentLength := int(binary.LittleEndian.Uint16(patched[offset+32:]))
		end := offset + centralHeaderSize + nameLength + extraLength + commentLength
		if end > len(patched) {
			t.Fatalf("invalid central ZIP header at offset %d", offset)
		}
		name := string(patched[offset+centralHeaderSize : offset+centralHeaderSize+nameLength])
		if size, ok := sizes[name]; ok {
			binary.LittleEndian.PutUint32(patched[offset+24:], size)
			delete(sizes, name)
		}
		offset = end
	}
	if len(sizes) != 0 {
		t.Fatalf("ZIP entries not found while patching sizes: %#v", sizes)
	}
	return patched
}
