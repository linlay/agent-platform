package catalog

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	EditableSkillPackageMaxUploadBytes  int64 = 512 << 20
	EditableSkillPackageMaxArchiveBytes int64 = 512 << 20
	EditableSkillPackageMaxArchiveFiles       = 8192

	skillPackageStateDirName        = ".package"
	skillPackageImportStagingPrefix = ".skill-package-import-"
	skillPackageBackupPrefix        = ".skill-package-backup-"
)

var (
	ErrSkillPackageNotFound      = errors.New("skill package not found")
	ErrSkillPackageSkillNotFound = errors.New("skill package child not found")
	ErrSkillPackageConflict      = errors.New("skill package conflicts with existing skills")
)

type SkillPackageRecordSkill struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

type SkillPackageRecord struct {
	SchemaVersion int                       `json:"schemaVersion"`
	ID            string                    `json:"id"`
	Version       string                    `json:"version"`
	SHA256        string                    `json:"sha256"`
	Skills        []SkillPackageRecordSkill `json:"skills"`
	InstalledAt   int64                     `json:"installedAt"`
}

type skillPackageManifest struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Type          string                      `json:"type"`
	ID            string                      `json:"id"`
	Version       string                      `json:"version"`
	Skills        []skillPackageManifestSkill `json:"skills"`
}

type skillPackageManifestSkill struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Path     string `json:"path"`
	Optional bool   `json:"optional,omitempty"`
}

type preparedPackageSkill struct {
	ID      string
	Version string
	Root    string
}

type EditableSkillPackageMutation struct {
	root            string
	recordPath      string
	oldRecord       []byte
	oldRecordExists bool
	backupRoot      string
	stagingRoot     string
	affectedIDs     []string
	unlock          func()
	done            bool
}

func (m *EditableSkillPackageMutation) Rollback() error {
	if m == nil || m.done {
		return nil
	}
	defer m.release()
	var rollbackErr error
	for _, id := range m.affectedIDs {
		if err := os.RemoveAll(filepath.Join(m.root, id)); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
		backupPath := filepath.Join(m.backupRoot, id)
		if _, err := os.Lstat(backupPath); err == nil {
			if err := os.Rename(backupPath, filepath.Join(m.root, id)); err != nil && rollbackErr == nil {
				rollbackErr = err
			}
		} else if !errors.Is(err, os.ErrNotExist) && rollbackErr == nil {
			rollbackErr = err
		}
	}
	if m.oldRecordExists {
		if err := writeSkillPackageRecordFile(m.recordPath, m.oldRecord); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	} else if err := os.Remove(m.recordPath); err != nil && !errors.Is(err, os.ErrNotExist) && rollbackErr == nil {
		rollbackErr = err
	}
	if err := os.RemoveAll(m.backupRoot); err != nil && rollbackErr == nil {
		rollbackErr = err
	}
	if err := os.RemoveAll(m.stagingRoot); err != nil && rollbackErr == nil {
		rollbackErr = err
	}
	m.done = true
	return rollbackErr
}

func (m *EditableSkillPackageMutation) Commit() error {
	if m == nil || m.done {
		return nil
	}
	defer m.release()
	// The new child directories and package record are already committed.
	// Cleanup is best effort and stale hidden transaction directories are
	// removed again during the next registry startup.
	_ = os.RemoveAll(m.backupRoot)
	_ = os.RemoveAll(m.stagingRoot)
	m.done = true
	return nil
}

func (m *EditableSkillPackageMutation) release() {
	if m.unlock != nil {
		m.unlock()
		m.unlock = nil
	}
}

func (r *FileRegistry) BeginImportEditableSkillPackageArchive(packageID, version string, source io.ReaderAt, size int64) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, SkillPackageRecord{}, fmt.Errorf("skills center directory is not configured")
	}
	packageID = strings.TrimSpace(packageID)
	version = strings.TrimSpace(version)
	if err := ValidateEditableSkillKey(packageID); err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if version == "" || source == nil || size <= 0 {
		return nil, SkillPackageRecord{}, ErrSkillArchiveInvalid
	}
	if size > EditableSkillPackageMaxUploadBytes {
		return nil, SkillPackageRecord{}, ErrSkillArchiveUploadTooLarge
	}
	r.skillPackageMu.Lock()
	lockOwnedByMutation := false
	defer func() {
		if !lockOwnedByMutation {
			r.skillPackageMu.Unlock()
		}
	}()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, SkillPackageRecord{}, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, SkillPackageRecord{}, ErrSkillSymlink
	}
	if !rootInfo.IsDir() {
		return nil, SkillPackageRecord{}, fmt.Errorf("%w: skills center is not a directory", ErrInvalidSkillPath)
	}
	archiveSHA256, err := skillPackageArchiveSHA256(source, size)
	if err != nil {
		return nil, SkillPackageRecord{}, ErrSkillArchiveInvalid
	}
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return nil, SkillPackageRecord{}, ErrSkillArchiveInvalid
	}
	entries, err := planEditableSkillPackageArchive(reader.File)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	stagingRoot, err := os.MkdirTemp(root, skillPackageImportStagingPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	var extractedBytes int64
	for _, entry := range entries {
		written, extractErr := extractSafeArchiveEntry(stagingRoot, entry, EditableSkillPackageMaxArchiveBytes-extractedBytes, editableSkillPackageArchivePolicy())
		if extractErr != nil {
			return nil, SkillPackageRecord{}, extractErr
		}
		extractedBytes += written
	}
	manifest, prepared, err := validatePreparedSkillPackage(stagingRoot, packageID, version)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	oldRecord, oldRecordBytes, oldRecordExists, err := readSkillPackageRecord(root, packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	owners, err := readSkillPackageOwners(root)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	newIDs := make(map[string]struct{}, len(prepared))
	for _, skill := range prepared {
		newIDs[skill.ID] = struct{}{}
		if owner := owners[skill.ID]; owner != "" && owner != packageID {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w: skill %s is owned by package %s", ErrSkillPackageConflict, skill.ID, owner)
		}
		target := filepath.Join(root, skill.ID)
		if _, statErr := os.Lstat(target); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, SkillPackageRecord{}, statErr
		}
	}
	for _, skill := range oldRecord.Skills {
		if _, retained := newIDs[skill.ID]; retained {
			continue
		}
		if usage := r.skillUsageByAgent()[skill.ID]; len(usage) > 0 {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w: removed skill %s is used by agents", ErrSkillPackageConflict, skill.ID)
		}
	}
	affectedSet := make(map[string]struct{}, len(oldRecord.Skills)+len(prepared))
	for _, skill := range oldRecord.Skills {
		affectedSet[skill.ID] = struct{}{}
	}
	for _, skill := range prepared {
		affectedSet[skill.ID] = struct{}{}
	}
	affectedIDs := make([]string, 0, len(affectedSet))
	for id := range affectedSet {
		affectedIDs = append(affectedIDs, id)
	}
	sort.Strings(affectedIDs)
	backupRoot, err := os.MkdirTemp(root, skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	recordPath, err := skillPackageRecordPath(root, packageID)
	if err != nil {
		_ = os.RemoveAll(backupRoot)
		return nil, SkillPackageRecord{}, err
	}
	mutation := &EditableSkillPackageMutation{
		root: root, recordPath: recordPath, oldRecord: oldRecordBytes, oldRecordExists: oldRecordExists,
		backupRoot: backupRoot, stagingRoot: stagingRoot, affectedIDs: affectedIDs,
		unlock: r.skillPackageMu.Unlock,
	}
	lockOwnedByMutation = true
	rollbackOnError := func(cause error) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
		if rollbackErr := mutation.Rollback(); rollbackErr != nil {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
		}
		return nil, SkillPackageRecord{}, cause
	}
	for _, id := range affectedIDs {
		target := filepath.Join(root, id)
		if _, statErr := os.Lstat(target); statErr == nil {
			if err := os.Rename(target, filepath.Join(backupRoot, id)); err != nil {
				return rollbackOnError(err)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return rollbackOnError(statErr)
		}
	}
	for _, skill := range prepared {
		if err := os.Rename(skill.Root, filepath.Join(root, skill.ID)); err != nil {
			return rollbackOnError(err)
		}
	}
	record := SkillPackageRecord{
		SchemaVersion: 1,
		ID:            packageID,
		Version:       version,
		SHA256:        archiveSHA256,
		Skills:        make([]SkillPackageRecordSkill, 0, len(prepared)),
		InstalledAt:   time.Now().UnixMilli(),
	}
	for _, skill := range prepared {
		record.Skills = append(record.Skills, SkillPackageRecordSkill{ID: skill.ID, Version: skill.Version})
	}
	sort.Slice(record.Skills, func(i, j int) bool { return record.Skills[i].ID < record.Skills[j].ID })
	if manifest.Version != version {
		return rollbackOnError(ErrSkillArchiveInvalid)
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return rollbackOnError(err)
	}
	encoded = append(encoded, '\n')
	if err := writeSkillPackageRecordFile(recordPath, encoded); err != nil {
		return rollbackOnError(err)
	}
	cleanupStaging = false
	return mutation, record, nil
}

func (r *FileRegistry) BeginDeleteEditableSkillPackage(packageID string) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, SkillPackageRecord{}, fmt.Errorf("skills center directory is not configured")
	}
	packageID = strings.TrimSpace(packageID)
	if err := ValidateEditableSkillKey(packageID); err != nil {
		return nil, SkillPackageRecord{}, err
	}
	r.skillPackageMu.Lock()
	lockOwnedByMutation := false
	defer func() {
		if !lockOwnedByMutation {
			r.skillPackageMu.Unlock()
		}
	}()
	record, recordBytes, exists, err := readSkillPackageRecord(root, packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if !exists {
		return nil, SkillPackageRecord{}, ErrSkillPackageNotFound
	}
	for _, skill := range record.Skills {
		if usage := r.skillUsageByAgent()[skill.ID]; len(usage) > 0 {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w: skill %s is used by agents", ErrSkillPackageConflict, skill.ID)
		}
	}
	backupRoot, err := os.MkdirTemp(root, skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	recordPath, err := skillPackageRecordPath(root, packageID)
	if err != nil {
		_ = os.RemoveAll(backupRoot)
		return nil, SkillPackageRecord{}, err
	}
	affectedIDs := make([]string, 0, len(record.Skills))
	for _, skill := range record.Skills {
		affectedIDs = append(affectedIDs, skill.ID)
	}
	sort.Strings(affectedIDs)
	mutation := &EditableSkillPackageMutation{
		root: root, recordPath: recordPath, oldRecord: recordBytes, oldRecordExists: true,
		backupRoot: backupRoot, affectedIDs: affectedIDs,
		unlock: r.skillPackageMu.Unlock,
	}
	lockOwnedByMutation = true
	for _, id := range affectedIDs {
		target := filepath.Join(root, id)
		if _, statErr := os.Lstat(target); statErr == nil {
			if err := os.Rename(target, filepath.Join(backupRoot, id)); err != nil {
				_ = mutation.Rollback()
				return nil, SkillPackageRecord{}, err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			_ = mutation.Rollback()
			return nil, SkillPackageRecord{}, statErr
		}
	}
	if err := os.Remove(recordPath); err != nil {
		_ = mutation.Rollback()
		return nil, SkillPackageRecord{}, err
	}
	return mutation, record, nil
}

func (r *FileRegistry) BeginDeleteEditableSkillPackageSkill(packageID, skillID string) (*EditableSkillPackageMutation, SkillPackageRecord, bool, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, false, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, SkillPackageRecord{}, false, fmt.Errorf("skills center directory is not configured")
	}
	packageID = strings.TrimSpace(packageID)
	skillID = strings.TrimSpace(skillID)
	if err := ValidateEditableSkillKey(packageID); err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	if err := ValidateEditableSkillKey(skillID); err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	r.skillPackageMu.Lock()
	lockOwnedByMutation := false
	defer func() {
		if !lockOwnedByMutation {
			r.skillPackageMu.Unlock()
		}
	}()
	record, recordBytes, exists, err := readSkillPackageRecord(root, packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	if !exists {
		return nil, SkillPackageRecord{}, false, ErrSkillPackageNotFound
	}
	remaining := make([]SkillPackageRecordSkill, 0, len(record.Skills)-1)
	found := false
	for _, skill := range record.Skills {
		if skill.ID == skillID {
			found = true
			continue
		}
		remaining = append(remaining, skill)
	}
	if !found {
		return nil, SkillPackageRecord{}, false, ErrSkillPackageSkillNotFound
	}
	if usage := r.skillUsageByAgent()[skillID]; len(usage) > 0 {
		return nil, SkillPackageRecord{}, false, fmt.Errorf("%w: skill %s is used by agents", ErrSkillPackageConflict, skillID)
	}
	backupRoot, err := os.MkdirTemp(root, skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	recordPath, err := skillPackageRecordPath(root, packageID)
	if err != nil {
		_ = os.RemoveAll(backupRoot)
		return nil, SkillPackageRecord{}, false, err
	}
	mutation := &EditableSkillPackageMutation{
		root: root, recordPath: recordPath, oldRecord: recordBytes, oldRecordExists: true,
		backupRoot: backupRoot, affectedIDs: []string{skillID}, unlock: r.skillPackageMu.Unlock,
	}
	lockOwnedByMutation = true
	rollbackOnError := func(cause error) (*EditableSkillPackageMutation, SkillPackageRecord, bool, error) {
		if rollbackErr := mutation.Rollback(); rollbackErr != nil {
			return nil, SkillPackageRecord{}, false, fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
		}
		return nil, SkillPackageRecord{}, false, cause
	}
	target := filepath.Join(root, skillID)
	if _, statErr := os.Lstat(target); statErr == nil {
		if err := os.Rename(target, filepath.Join(backupRoot, skillID)); err != nil {
			return rollbackOnError(err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return rollbackOnError(statErr)
	}
	record.Skills = remaining
	packageDeleted := len(remaining) == 0
	if packageDeleted {
		if err := os.Remove(recordPath); err != nil {
			return rollbackOnError(err)
		}
	} else {
		encoded, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return rollbackOnError(err)
		}
		if err := writeSkillPackageRecordFile(recordPath, append(encoded, '\n')); err != nil {
			return rollbackOnError(err)
		}
	}
	return mutation, record, packageDeleted, nil
}

func (r *FileRegistry) EditableSkillPackages() ([]SkillPackageRecord, error) {
	if r == nil {
		return nil, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, fmt.Errorf("skills center directory is not configured")
	}
	r.skillPackageMu.Lock()
	defer r.skillPackageMu.Unlock()
	dir := filepath.Join(root, skillPackageStateDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []SkillPackageRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]SkillPackageRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		packageID := strings.TrimSuffix(entry.Name(), ".json")
		record, _, exists, err := readSkillPackageRecord(root, packageID)
		if err != nil {
			return nil, err
		}
		if exists {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

func planEditableSkillPackageArchive(files []*zip.File) ([]safeArchiveEntry, error) {
	policy := editableSkillPackageArchivePolicy()
	candidates, err := collectSafeArchiveCandidates(files, policy)
	if err != nil {
		return nil, err
	}
	return finalizeSafeArchiveEntries(candidates, "", policy)
}

func editableSkillPackageArchivePolicy() safeArchivePolicy {
	return safeArchivePolicy{
		subject: "skill package", maxFiles: EditableSkillPackageMaxArchiveFiles,
		maxFileBytes: EditableSkillMaxUploadBytes, maxArchiveBytes: EditableSkillPackageMaxArchiveBytes,
		tooManyFiles: ErrSkillArchiveTooManyFiles, fileTooLarge: ErrSkillFileTooLarge,
		archiveTooLarge: ErrSkillArchiveTooLarge, validationError: skillArchiveValidationError,
		directoryMode: func(string) fs.FileMode { return 0o755 },
		parentMode:    func(string) fs.FileMode { return 0o755 },
		fileMode: func(_ string, archiveMode fs.FileMode) fs.FileMode {
			mode := archiveMode.Perm()
			if mode == 0 {
				mode = 0o644
			}
			return mode | 0o600
		},
		validateFile: validateEditableSkillSpecialFile,
	}
}

func validatePreparedSkillPackage(stagingRoot, expectedID, expectedVersion string) (skillPackageManifest, []preparedPackageSkill, error) {
	manifestPath := filepath.Join(stagingRoot, "manifest.json")
	content, err := os.ReadFile(manifestPath)
	if err != nil || int64(len(content)) > EditableSkillMaxTextBytes {
		return skillPackageManifest{}, nil, skillArchiveValidationError("invalid_package_manifest", "manifest.json is required and must be valid", "manifest.json")
	}
	var manifest skillPackageManifest
	if err := json.Unmarshal(content, &manifest); err != nil || manifest.SchemaVersion != 1 || strings.TrimSpace(manifest.Type) != "skill-package" || strings.TrimSpace(manifest.ID) != expectedID || strings.TrimSpace(manifest.Version) != expectedVersion || len(manifest.Skills) == 0 {
		return skillPackageManifest{}, nil, skillArchiveValidationError("invalid_package_manifest", "manifest.json does not match the requested skill package", "manifest.json")
	}
	seen := map[string]struct{}{}
	prepared := make([]preparedPackageSkill, 0, len(manifest.Skills))
	for _, entry := range manifest.Skills {
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Version = strings.TrimSpace(entry.Version)
		entry.Path = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(entry.Path)), "/")
		if err := ValidateEditableSkillKey(entry.ID); err != nil || entry.Version == "" || entry.Path != "skills/"+entry.ID {
			return skillPackageManifest{}, nil, skillArchiveValidationError("invalid_package_skill", "skill package entry is invalid", entry.Path)
		}
		if _, duplicate := seen[strings.ToLower(entry.ID)]; duplicate {
			return skillPackageManifest{}, nil, skillArchiveValidationError("duplicate_package_skill", "skill package contains duplicate skill IDs", entry.Path)
		}
		seen[strings.ToLower(entry.ID)] = struct{}{}
		root, err := resolvePreparedPackageSkillRoot(filepath.Join(stagingRoot, filepath.FromSlash(entry.Path)))
		if errors.Is(err, os.ErrNotExist) && entry.Optional {
			continue
		}
		if err != nil {
			return skillPackageManifest{}, nil, skillArchiveValidationError("missing_package_skill", "required skill is missing from the package", entry.Path)
		}
		if err := validateImportedEditableSkill(root); err != nil {
			return skillPackageManifest{}, nil, err
		}
		prepared = append(prepared, preparedPackageSkill{ID: entry.ID, Version: entry.Version, Root: root})
	}
	if len(prepared) == 0 {
		return skillPackageManifest{}, nil, skillArchiveValidationError("empty_skill_package", "skill package does not contain installable skills", "manifest.json")
	}
	return manifest, prepared, nil
}

func resolvePreparedPackageSkillRoot(root string) (string, error) {
	if info, err := os.Lstat(filepath.Join(root, "SKILL.md")); err == nil && info.Mode().IsRegular() {
		return root, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	directories := []string{}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			directories = append(directories, entry.Name())
		}
	}
	if len(directories) != 1 {
		return "", os.ErrNotExist
	}
	nested := filepath.Join(root, directories[0])
	if info, err := os.Lstat(filepath.Join(nested, "SKILL.md")); err == nil && info.Mode().IsRegular() {
		return nested, nil
	}
	return "", os.ErrNotExist
}

func skillPackageRecordPath(root, packageID string) (string, error) {
	if err := ValidateEditableSkillKey(strings.TrimSpace(packageID)); err != nil {
		return "", err
	}
	dir := filepath.Join(root, skillPackageStateDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrSkillSymlink
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: skill package state root is not a directory", ErrInvalidSkillPath)
	}
	return filepath.Join(dir, strings.TrimSpace(packageID)+".json"), nil
}

func readSkillPackageRecord(root, packageID string) (SkillPackageRecord, []byte, bool, error) {
	path, err := skillPackageRecordPath(root, packageID)
	if err != nil {
		return SkillPackageRecord{}, nil, false, err
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SkillPackageRecord{}, nil, false, nil
	}
	if err != nil {
		return SkillPackageRecord{}, nil, false, err
	}
	var record SkillPackageRecord
	if err := json.Unmarshal(content, &record); err != nil ||
		record.SchemaVersion != 1 ||
		record.ID != strings.TrimSpace(packageID) ||
		strings.TrimSpace(record.Version) == "" ||
		strings.TrimSpace(record.SHA256) == "" ||
		record.InstalledAt <= 0 ||
		len(record.Skills) == 0 {
		return SkillPackageRecord{}, nil, false, fmt.Errorf("invalid skill package record %s", path)
	}
	seen := make(map[string]struct{}, len(record.Skills))
	for _, skill := range record.Skills {
		if err := ValidateEditableSkillKey(skill.ID); err != nil || strings.TrimSpace(skill.Version) == "" {
			return SkillPackageRecord{}, nil, false, fmt.Errorf("invalid skill package record %s", path)
		}
		key := strings.ToLower(strings.TrimSpace(skill.ID))
		if _, duplicate := seen[key]; duplicate {
			return SkillPackageRecord{}, nil, false, fmt.Errorf("invalid skill package record %s", path)
		}
		seen[key] = struct{}{}
	}
	return record, content, true, nil
}

func readSkillPackageOwners(root string) (map[string]string, error) {
	dir := filepath.Join(root, skillPackageStateDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		packageID := strings.TrimSuffix(entry.Name(), ".json")
		record, _, exists, err := readSkillPackageRecord(root, packageID)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		for _, skill := range record.Skills {
			if previous := owners[skill.ID]; previous != "" && previous != record.ID {
				return nil, fmt.Errorf("%w: skill %s has multiple package owners", ErrSkillPackageConflict, skill.ID)
			}
			owners[skill.ID] = record.ID
		}
	}
	return owners, nil
}

func writeSkillPackageRecordFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".skill-package-record-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func skillPackageArchiveSHA256(source io.ReaderAt, size int64) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(source, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
