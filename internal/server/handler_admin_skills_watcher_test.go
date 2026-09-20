package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/reload"
)

func TestAdminSkillMutationsWithBackgroundWatcher(t *testing.T) {
	fixture := newTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload.StartBackgroundReloaders(ctx, fixture.cfg, fixture.catalogReloader)
	s := fixture.server
	const key = "ordinary-watched"
	markdown := "---\nname: ordinary-watched\ndescription: Ordinary watched skill\n---\n\nContent.\n"
	if _, err := s.createAdminSkill(ctx, api.CreateAdminSkillRequest{Key: key, SkillMd: markdown}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mkdirAdminSkillFile(ctx, api.MkdirAdminSkillFileRequest{Key: key, Path: "references"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createAdminSkillFile(ctx, api.CreateAdminSkillFileRequest{Key: key, Path: "references/example.md", Content: "original"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writeAdminSkillFile(ctx, api.WriteAdminSkillFileRequest{Key: key, Path: "references/example.md", Content: "saved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.uploadAdminSkillFile(ctx, key, "references/upload.md", strings.NewReader("uploaded"), false); err != nil {
		t.Fatal(err)
	}
	// The references directory has been watched since the previous transaction
	// resumed; this rename exercises the Windows directory-handle conflict.
	if _, err := s.renameAdminSkillFile(ctx, api.RenameAdminSkillFileRequest{Key: key, FromPath: "references", ToPath: "renamed"}); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"example.md": "saved", "upload.md": "uploaded"} {
		content, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, key, "renamed", name))
		if err != nil || string(content) != expected {
			t.Fatalf("renamed content %s: %q, %v", name, content, err)
		}
	}
	if _, err := s.deleteAdminSkillFile(ctx, api.DeleteAdminSkillFileRequest{Key: key, Path: "renamed", Recursive: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, key, "SKILL.md")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(markdown, "Ordinary watched skill", "Live after file changes")), 0o644); err != nil {
		t.Fatal(err)
	}
	awaitWatchedSkillDescription(t, fixture.registry, key, "Live after file changes")
	if _, err := s.deleteAdminSkill(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("deleted skill remains: %v", err)
	}
	archive := serverSkillImportZIP(t, map[string]string{"SKILL.md": markdown, "references/nested/readme.md": "Imported"})
	if _, err := s.importAdminSkill(ctx, key, bytes.NewReader(archive), int64(len(archive))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(markdown, "Ordinary watched skill", "Live after import")), 0o644); err != nil {
		t.Fatal(err)
	}
	awaitWatchedSkillDescription(t, fixture.registry, key, "Live after import")
	if _, err := s.deleteAdminSkill(ctx, key); err != nil {
		t.Fatal(err)
	}
}
