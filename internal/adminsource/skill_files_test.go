package adminsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func skillFileEditor(t *testing.T) (*catalog.FileRegistry, string) {
	t.Helper()
	root := t.TempDir()
	skills := filepath.Join(root, "skills-center")
	for _, dir := range []string{skills, filepath.Join(root, "agents"), filepath.Join(root, "teams")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := catalog.NewFileRegistry(config.Config{Paths: config.PathsConfig{SkillsCenterDir: skills, AgentsDir: filepath.Join(root, "agents"), TeamsDir: filepath.Join(root, "teams"), StateDir: filepath.Join(root, ".state")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CreateEditableSkill("demo", "# Original\n", nil); err != nil {
		t.Fatal(err)
	}
	return registry, skills
}

func TestWriteSkillFilePreservesExternalChangeDuringFailedReload(t *testing.T) {
	editor, root := skillFileEditor(t)
	before, err := editor.ReadEditableSkillFile("demo", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	reloads := 0
	_, err = NewService().WriteSkillFile(context.Background(), editor, "demo", "SKILL.md", "# Our edit\n", "utf-8", before.SHA256, func(context.Context) error {
		reloads++
		if err := os.WriteFile(filepath.Join(root, "demo", "SKILL.md"), []byte("# External edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errors.New("publication failed")
	})
	var failure *SkillFileReloadError
	if !errors.As(err, &failure) || !errors.Is(failure.RollbackErr, catalog.ErrSkillConflict) || reloads != 1 {
		t.Fatalf("unexpected rollback result: %v (reloads %d)", err, reloads)
	}
	after, err := editor.ReadEditableSkillFile("demo", "SKILL.md")
	if err != nil || after.Content != "# External edit\n" {
		t.Fatalf("rollback overwrote external changes: %#v %v", after, err)
	}
}

func TestWriteSkillFileRemovesNewFileOnFailedReload(t *testing.T) {
	editor, _ := skillFileEditor(t)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := 0
	_, err := NewService().WriteSkillFile(ctx, editor, "demo", "new.txt", "new file\n", "utf-8", "", func(ctx context.Context) error {
		reloads++
		cancel()
		if ctx.Err() != nil {
			t.Fatal("client cancellation reached publication or rollback recovery")
		}
		if reloads == 1 {
			return errors.New("publication failed")
		}
		return nil
	})
	var failure *SkillFileReloadError
	if !errors.As(err, &failure) || failure.RollbackErr != nil || failure.ReloadErr != nil || reloads != 2 {
		t.Fatalf("unexpected rollback result: %v (reloads %d)", err, reloads)
	}
	if _, err := editor.ReadEditableSkillFile("demo", "new.txt"); !errors.Is(err, catalog.ErrSkillNotFound) {
		t.Fatalf("failed save left a new file: %v", err)
	}
}

func TestWriteSkillFileReportsRecoveryFailureAfterRestoringBytes(t *testing.T) {
	editor, _ := skillFileEditor(t)
	before, err := editor.ReadEditableSkillFile("demo", "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService().WriteSkillFile(context.Background(), editor, "demo", "SKILL.md", "# Our edit\n", "utf-8", before.SHA256, func(context.Context) error {
		return errors.New("connector catalog unavailable")
	})
	var failure *SkillFileReloadError
	if !errors.As(err, &failure) || failure.RollbackErr != nil || failure.ReloadErr == nil || !strings.Contains(err.Error(), "previous skill source restored") {
		t.Fatalf("unexpected recovery result: %v", err)
	}
	after, err := editor.ReadEditableSkillFile("demo", "SKILL.md")
	if err != nil || after.Content != before.Content || after.SHA256 != before.SHA256 {
		t.Fatalf("source was not restored: %#v %v", after, err)
	}
}
