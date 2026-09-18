package catalog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EditableSkillImportMutation retains the old directory until catalog reload
// succeeds. It shares the successful-move journal used by package transactions.
type EditableSkillImportMutation struct{ *EditableSkillPackageMutation }

func (r *FileRegistry) BeginImportEditableSkillArchive(key string, source io.ReaderAt, size int64, overwrite bool) (*EditableSkillImportMutation, AdminSkill, error) {
	if r == nil {
		return nil, AdminSkill{}, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, AdminSkill{}, fmt.Errorf("skills center directory is not configured")
	}
	key = strings.TrimSpace(key)
	if err := ValidateEditableSkillKey(key); err != nil {
		return nil, AdminSkill{}, err
	}
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, AdminSkill{}, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, AdminSkill{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, AdminSkill{}, ErrSkillSymlink
	}
	if !info.IsDir() {
		return nil, AdminSkill{}, ErrInvalidSkillPath
	}
	target := filepath.Join(root, key)
	hadPrevious := false
	if info, err := os.Lstat(target); err == nil {
		if !overwrite {
			return nil, AdminSkill{}, ErrSkillAlreadyExists
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, AdminSkill{}, ErrSkillSymlink
		}
		if !info.IsDir() {
			return nil, AdminSkill{}, ErrInvalidSkillPath
		}
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, AdminSkill{}, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(filepath.Clean(root)), editableSkillImportStagingPrefix)
	if err != nil {
		return nil, AdminSkill{}, err
	}
	defer os.RemoveAll(stage)
	candidate, err := importEditableSkillArchiveIntoRoot(stage, key, source, size)
	if err != nil {
		return nil, AdminSkill{}, err
	}
	// Validate both the archive and its catalog interpretation before touching
	// the installed directory, including skill.json and all runtime metadata.
	item, err := buildAdminSkill(stage, key, nil, true)
	if err != nil {
		return nil, AdminSkill{}, err
	}
	if item.Status != AdminSkillStatusReady {
		diagnostics := make([]SkillArchiveDiagnostic, 0, len(item.Diagnostics))
		for _, diagnostic := range item.Diagnostics {
			diagnostics = append(diagnostics, SkillArchiveDiagnostic{Code: diagnostic.Code, Message: diagnostic.Message, SourcePath: archiveDiagnosticRelativePath(candidate, diagnostic.SourcePath)})
		}
		return nil, AdminSkill{}, &SkillArchiveValidationError{Diagnostics: diagnostics}
	}
	backup, err := os.MkdirTemp(filepath.Dir(filepath.Clean(root)), ".skill-backup-")
	if err != nil {
		return nil, AdminSkill{}, err
	}
	mutation := &EditableSkillImportMutation{&EditableSkillPackageMutation{root: root, stagingRoot: stage, backupRoot: backup, unlock: r.skillPackageMu.Unlock}}
	owned = true
	fail := func(cause error) (*EditableSkillImportMutation, AdminSkill, error) {
		return nil, AdminSkill{}, errors.Join(cause, mutation.Rollback())
	}
	if hadPrevious {
		if err := mutation.backupSkill(key); err != nil {
			return fail(err)
		}
	}
	if err := mutation.publishSkill(key, candidate); err != nil {
		return fail(err)
	}
	item, err = buildAdminSkill(root, key, r.skillUsageByAgent()[key], true)
	if err != nil {
		return fail(err)
	}
	return mutation, item, nil
}

// EditableSkillDeleteMutation keeps the removed source until reload succeeds.
type EditableSkillDeleteMutation struct{ *EditableSkillPackageMutation }

func (r *FileRegistry) BeginDeleteEditableSkill(key string) (*EditableSkillDeleteMutation, error) {
	if r == nil {
		return nil, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, fmt.Errorf("skills center directory is not configured")
	}
	key = strings.TrimSpace(key)
	if err := ValidateEditableSkillKey(key); err != nil {
		return nil, err
	}
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSkillNotFound
	}
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillSymlink
	}
	if !rootInfo.IsDir() {
		return nil, ErrInvalidSkillPath
	}
	owners, err := readSkillPackageOwners(root)
	if err != nil {
		return nil, err
	}
	if owner := owners[key]; owner != "" {
		return nil, fmt.Errorf("%w: skill %s belongs to package %s", ErrSkillPackageConflict, key, owner)
	}
	dir, err := editableSkillDir(root, key)
	if err != nil {
		return nil, err
	}
	if err := ensureNoSymlinkAlongExistingPath(root, dir); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSkillNotFound
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillSymlink
	}
	if !info.IsDir() {
		return nil, ErrInvalidSkillPath
	}
	backup, err := os.MkdirTemp(filepath.Dir(filepath.Clean(root)), ".skill-backup-")
	if err != nil {
		return nil, err
	}
	mutation := &EditableSkillDeleteMutation{&EditableSkillPackageMutation{root: root, backupRoot: backup, unlock: r.skillPackageMu.Unlock}}
	owned = true
	if err := mutation.backupSkill(key); err != nil {
		return nil, errors.Join(err, mutation.Rollback())
	}
	return mutation, nil
}
