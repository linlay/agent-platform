package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// MigrateLegacySkillPackages is an explicit upgrade step. It copies legacy
// flat members into their package without removing the original standalone
// skills, preserving existing short-key Agent references. The old membership
// record is retained in a recovery backup, outside the watched skill root.
func (r *FileRegistry) MigrateLegacySkillPackages() ([]string, error) {
	if r == nil {
		return nil, ErrSkillPackageNotFound
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, ErrInvalidSkillPath
	}
	dir := filepath.Join(root, skillPackageStateDirName)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillSymlink
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	backups := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		mutation, err := r.BeginMigrateLegacySkillPackage(id)
		if errors.Is(err, ErrSkillPackageConflict) {
			log.Printf("[catalog][skills] warning code=skill_package_migration_conflict package=%q source=%q error=%v; legacy record and existing content retained", id, filepath.Join(dir, entry.Name()), err)
			continue
		}
		if err != nil {
			return backups, err
		}
		if mutation == nil {
			continue
		}
		backup := mutation.backupRoot
		if err := mutation.Commit(); err != nil {
			return backups, err
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

func (r *FileRegistry) BeginMigrateLegacySkillPackage(id string) (*EditableSkillPackageMutation, error) {
	if r == nil {
		return nil, ErrSkillPackageNotFound
	}
	if err := ValidateSkillPackageID(id); err != nil {
		return nil, err
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	record, _, exists, err := readLegacySkillPackageRecord(root, id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	// Never merge into an existing independently installed package or skill.
	if _, err := os.Lstat(filepath.Join(root, id)); err == nil {
		return nil, fmt.Errorf("%w: migration destination %s already exists", ErrSkillPackageConflict, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), skillPackageImportStagingPrefix)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	candidate := filepath.Join(stage, id)
	if err := os.Mkdir(candidate, 0o755); err != nil {
		return nil, err
	}
	for _, child := range record.Skills {
		if err := ValidateSkillPackageID(child.ID); err != nil {
			return nil, err
		}
		source := filepath.Join(root, child.ID)
		if _, err := os.Lstat(filepath.Join(source, "SKILL.md")); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := copyRuntimePath(source, filepath.Join(candidate, child.ID)); err != nil {
			return nil, err
		}
	}
	manifest := SkillPackageMetadata{Name: id, DisplayName: record.Name, Version: record.Version}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(candidate, "package.json"), append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	if _, err := scanPackageAt(candidate, id); err != nil {
		return nil, err
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), skillPackageBackupPrefix)
	if err != nil {
		return nil, err
	}
	mutation := &EditableSkillPackageMutation{root: root, backupRoot: backup, stagingRoot: stage, keepBackup: true, unlock: r.skillPackageMu.Unlock}
	owned = true
	if err := mutation.backupSkill(filepath.Join(skillPackageStateDirName, id+".json")); err != nil {
		return nil, errors.Join(err, mutation.Rollback())
	}
	if err := mutation.publishSkill(id, candidate); err != nil {
		return nil, errors.Join(err, mutation.Rollback())
	}
	return mutation, nil
}
