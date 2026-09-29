package catalog

import (
	"agent-platform/internal/skillmeta"
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
	Diagnostics []AdminSkillDiagnostic `json:"-"`
	ID          string                 `json:"id"`
	Name        string                 `json:"name,omitempty"`
	Path        string                 `json:"path,omitempty"`
	DisplayName string                 `json:"displayName,omitempty"`
	Description string                 `json:"description,omitempty"`
	Triggers    []string               `json:"triggers,omitempty"`
	Metadata    map[string]any         `json:"metadata,omitempty"`
	Version     string                 `json:"version,omitempty"`
}

type SkillPackageRecord struct {
	Presentation  skillmeta.Presentation    `json:"-"`
	Name          string                    `json:"name,omitempty"`
	DisplayName   string                    `json:"displayName,omitempty"`
	Description   string                    `json:"description,omitempty"`
	Triggers      []string                  `json:"triggers,omitempty"`
	Metadata      map[string]any            `json:"metadata,omitempty"`
	SchemaVersion int                       `json:"schemaVersion"`
	ID            string                    `json:"id"`
	Version       string                    `json:"version"`
	SHA256        string                    `json:"sha256"`
	Skills        []SkillPackageRecordSkill `json:"skills"`
	InstalledAt   int64                     `json:"installedAt"`
}

type skillPackageManifest struct {
	Name          string                      `json:"name,omitempty"`
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
	backedUpIDs     []string
	publishedIDs    []string
	recordChanged   bool
	unlock          func()
	done            bool
	keepBackup      bool
}

// These helpers journal a move only after it succeeds, including on Windows
// where a watched or externally held directory can reject a rename.
func (m *EditableSkillPackageMutation) backupSkill(id string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(m.backupRoot, id)), 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(m.root, id), filepath.Join(m.backupRoot, id)); err != nil {
		return err
	}
	m.backedUpIDs = append(m.backedUpIDs, id)
	return nil
}

func (m *EditableSkillPackageMutation) publishSkill(id, source string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(m.root, id)), 0o755); err != nil {
		return err
	}
	if err := os.Rename(source, filepath.Join(m.root, id)); err != nil {
		return err
	}
	m.publishedIDs = append(m.publishedIDs, id)
	return nil
}

func (m *EditableSkillPackageMutation) Rollback() error {
	if m == nil || m.done {
		return nil
	}
	defer m.release()
	var rollbackErr error
	// Only undo completed moves. A failed backup rename leaves the original in
	// place; removing every planned ID here would destroy that untouched skill.
	for _, id := range m.publishedIDs {
		if err := os.RemoveAll(filepath.Join(m.root, id)); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	for _, id := range m.backedUpIDs {
		target := filepath.Join(m.root, id)
		if _, err := os.Lstat(target); err == nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore skill %s: destination still exists", id))
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		if err := os.Rename(filepath.Join(m.backupRoot, id), target); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	if m.recordChanged {
		if m.oldRecordExists {
			rollbackErr = errors.Join(rollbackErr, writeSkillPackageRecordFile(m.recordPath, m.oldRecord))
		} else if err := os.Remove(m.recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	// Failed restores must retain their backup, rather than deleting the only
	// remaining copy of an old skill. Include the recovery path in the error.
	if rollbackErr == nil {
		rollbackErr = os.RemoveAll(m.backupRoot)
	} else {
		if m.oldRecordExists {
			rollbackErr = errors.Join(rollbackErr, os.WriteFile(filepath.Join(m.backupRoot, ".original-package-record.json"), m.oldRecord, 0o600))
		}
		rollbackErr = fmt.Errorf("%w; recovery backup retained at %s", rollbackErr, m.backupRoot)
	}
	rollbackErr = errors.Join(rollbackErr, os.RemoveAll(m.stagingRoot))
	m.done = true
	return rollbackErr
}

func (m *EditableSkillPackageMutation) Commit() error {
	if m == nil || m.done {
		return nil
	}
	defer m.release()
	// The new child directories and package record are already committed.
	// Cleanup is best effort. Sibling transaction directories remain outside
	// the catalog/watch root, including when Windows temporarily blocks cleanup.
	if !m.keepBackup {
		_ = os.RemoveAll(m.backupRoot)
	}
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

// PreparedEditableSkillPackage separates immutable ZIP validation from the
// installed-state checks and publication transaction.
type PreparedEditableSkillPackage struct {
	registry                                       *FileRegistry
	packageID, version, stagingRoot, archiveSHA256 string
	manifest                                       SkillPackageMetadata
	candidate                                      string
	skills                                         []preparedPackageSkill
}

func (p *PreparedEditableSkillPackage) Close() { _ = os.RemoveAll(p.stagingRoot) }

func (r *FileRegistry) PrepareEditableSkillPackageArchive(packageID, version string, source io.ReaderAt, size int64) (*PreparedEditableSkillPackage, error) {
	if r == nil {
		return nil, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, fmt.Errorf("skills center directory is not configured")
	}
	packageID = strings.TrimSpace(packageID)
	version = strings.TrimSpace(version)
	if err := ValidateSkillPackageID(packageID); err != nil {
		return nil, err
	}
	if source == nil || size <= 0 {
		return nil, ErrSkillArchiveInvalid
	}
	if size > EditableSkillPackageMaxUploadBytes {
		return nil, ErrSkillArchiveUploadTooLarge
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(root)), 0o755); err != nil {
		return nil, err
	}
	archiveSHA256, err := skillPackageArchiveSHA256(source, size)
	if err != nil {
		return nil, ErrSkillArchiveInvalid
	}
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return nil, ErrSkillArchiveInvalid
	}
	entries, err := planEditableSkillPackageArchive(reader.File)
	if err != nil {
		return nil, err
	}
	stagingRoot, err := os.MkdirTemp(filepath.Dir(filepath.Clean(root)), skillPackageImportStagingPrefix)
	if err != nil {
		return nil, err
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
			return nil, extractErr
		}
		extractedBytes += written
	}
	manifest, prepared, candidate, err := prepareNestedSkillPackage(stagingRoot, packageID, version)
	if err != nil {
		return nil, err
	}
	cleanupStaging = false
	return &PreparedEditableSkillPackage{registry: r, packageID: packageID, version: version, stagingRoot: stagingRoot, archiveSHA256: archiveSHA256, manifest: manifest, skills: prepared, candidate: candidate}, nil
}

func (r *FileRegistry) BeginImportEditableSkillPackageArchive(packageID, version string, source io.ReaderAt, size int64) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	prepared, err := r.PrepareEditableSkillPackageArchive(packageID, version, source, size)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	defer prepared.Close()
	return prepared.Begin()
}

func (p *PreparedEditableSkillPackage) Begin() (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	r := p.registry
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	if err := ensureSkillPackageRoot(root); err != nil {
		return nil, SkillPackageRecord{}, err
	}
	old, _, exists, err := readSkillPackageRecord(root, p.packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	target := filepath.Join(root, p.packageID)
	if _, err := os.Lstat(target); err == nil && !exists {
		return nil, SkillPackageRecord{}, fmt.Errorf("%w: destination %s is not a skill package", ErrSkillPackageConflict, p.packageID)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, SkillPackageRecord{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), p.packageID) && entry.Name() != p.packageID {
			return nil, SkillPackageRecord{}, ErrSkillPackageConflict
		}
	}
	next, err := scanPackageAt(p.candidate, p.packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	keep := map[string]bool{}
	for _, child := range next.Skills {
		keep[child.ID] = true
	}
	for _, child := range old.Skills {
		if !keep[child.ID] && len(r.skillUsageByAgent()[child.ID]) > 0 {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w: removed skill %s is used by agents", ErrSkillPackageConflict, child.ID)
		}
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	m := &EditableSkillPackageMutation{root: root, backupRoot: backup, stagingRoot: p.stagingRoot, unlock: r.skillPackageMu.Unlock}
	owned = true
	fail := func(err error) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
		return nil, SkillPackageRecord{}, errors.Join(err, m.Rollback())
	}
	if exists {
		if err := m.backupSkill(p.packageID); err != nil {
			return fail(err)
		}
	}
	if err := m.publishSkill(p.packageID, p.candidate); err != nil {
		return fail(err)
	}
	next.SHA256 = p.archiveSHA256
	return m, next, nil
}

func (r *FileRegistry) BeginDeleteEditableSkillPackage(packageID string) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, ErrSkillPackageNotFound
	}
	if err := ValidateSkillPackageID(packageID); err != nil {
		return nil, SkillPackageRecord{}, err
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	record, _, exists, err := readSkillPackageRecord(root, packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if !exists {
		return nil, SkillPackageRecord{}, ErrSkillPackageNotFound
	}
	for _, child := range record.Skills {
		if len(r.skillUsageByAgent()[child.ID]) > 0 {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w: skill %s is used by agents", ErrSkillPackageConflict, child.ID)
		}
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	m := &EditableSkillPackageMutation{root: root, backupRoot: backup, unlock: r.skillPackageMu.Unlock}
	owned = true
	if err := m.backupSkill(packageID); err != nil {
		return nil, SkillPackageRecord{}, errors.Join(err, m.Rollback())
	}
	return m, record, nil
}

func (r *FileRegistry) BeginDeleteEditableSkillPackageSkill(packageID, skillID string) (*EditableSkillPackageMutation, SkillPackageRecord, bool, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, false, ErrSkillPackageNotFound
	}
	if err := ValidateSkillPackageID(packageID); err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	name := strings.TrimPrefix(skillID, packageID+"/")
	if err := ValidateSkillPackageID(name); err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	key := packageID + "/" + name
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	record, original, exists, err := readSkillPackageRecord(root, packageID)
	if err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	if !exists {
		return nil, SkillPackageRecord{}, false, ErrSkillPackageNotFound
	}
	found := false
	remaining := make([]SkillPackageRecordSkill, 0, len(record.Skills))
	for _, child := range record.Skills {
		if child.ID == key {
			found = true
		} else {
			remaining = append(remaining, child)
		}
	}
	if !found {
		return nil, SkillPackageRecord{}, false, ErrSkillPackageSkillNotFound
	}
	if len(r.skillUsageByAgent()[key]) > 0 {
		return nil, SkillPackageRecord{}, false, fmt.Errorf("%w: skill %s is used by agents", ErrSkillPackageConflict, key)
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, false, err
	}
	m := &EditableSkillPackageMutation{root: root, backupRoot: backup, unlock: r.skillPackageMu.Unlock,
		recordPath: filepath.Join(root, packageID, "package.json"), oldRecord: original, oldRecordExists: true}
	owned = true
	if err := m.backupSkill(key); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, SkillPackageRecord{}, false, errors.Join(err, m.Rollback())
	}
	manifest, err := parseSkillPackageMetadata(original)
	if err != nil {
		return nil, SkillPackageRecord{}, false, errors.Join(err, m.Rollback())
	}
	members := make([]SkillPackageMember, 0, len(manifest.Skills))
	for _, member := range manifest.Skills {
		if member.Key != name {
			members = append(members, member)
		}
	}
	manifest.Skills = members
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		err = writeSkillPackageRecordFile(m.recordPath, append(data, '\n'))
	}
	if err != nil {
		return nil, SkillPackageRecord{}, false, errors.Join(err, m.Rollback())
	}
	m.recordChanged = true
	record.Skills = remaining
	return m, record, false, nil
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
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []SkillPackageRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := []SkillPackageRecord{}
	for _, entry := range entries {
		if !isSkillCenterDirectory(entry) {
			continue
		}
		record, _, exists, err := readSkillPackageRecord(root, entry.Name())
		if err != nil {
			logInvalidSkillPackage(root, entry.Name(), err)
			continue
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
	prefix := ""
	rootMarker := false
	for _, c := range candidates {
		if !c.dir && (c.path == "package.json" || c.path == "manifest.json" || c.path == "SKILL.md") {
			rootMarker = true
		}
	}
	if !rootMarker {
		first := ""
		same := true
		for _, c := range candidates {
			parts := strings.Split(c.path, "/")
			if len(parts) < 2 && !c.dir {
				same = false
				break
			}
			if first == "" {
				first = parts[0]
			} else if first != parts[0] {
				same = false
				break
			}
		}
		if same && first != "" {
			prefix = first + "/"
		}
	}
	return finalizeSafeArchiveEntries(candidates, prefix, policy)
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
		if err := ValidateSkillPackageID(entry.ID); err != nil || entry.Version == "" || entry.Path != "skills/"+entry.ID {
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
	if err := ValidateSkillPackageID(packageID); err != nil {
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

func readLegacySkillPackageRecord(root, packageID string) (SkillPackageRecord, []byte, bool, error) {
	path, err := skillPackageRecordPath(root, packageID)
	if err != nil {
		return SkillPackageRecord{}, nil, false, err
	}
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return SkillPackageRecord{}, nil, false, nil
	}
	if statErr != nil {
		return SkillPackageRecord{}, nil, false, statErr
	}
	if !info.Mode().IsRegular() {
		return SkillPackageRecord{}, nil, false, ErrSkillSymlink
	}
	if info.Size() > EditableSkillMaxTextBytes {
		return SkillPackageRecord{}, nil, false, ErrSkillFileTooLarge
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
		if err := ValidateSkillPackageID(skill.ID); err != nil || strings.TrimSpace(skill.Version) == "" {
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
	owners := map[string]string{}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return owners, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !isSkillCenterDirectory(e) {
			continue
		}
		record, _, exists, err := readSkillPackageRecord(root, e.Name())
		if err != nil {
			logInvalidSkillPackage(root, e.Name(), err)
			continue
		}
		if exists {
			for _, child := range record.Skills {
				owners[child.ID] = record.ID
			}
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

// Compare installed content with the package snapshot without changing either.
func skillPackageVersionDiagnostics(root string, record SkillPackageRecord) SkillPackageRecord {
	record.Skills = append([]SkillPackageRecordSkill(nil), record.Skills...)
	for i := range record.Skills {
		entry := &record.Skills[i]
		entry.Diagnostics = nil
		content, err := os.ReadFile(filepath.Join(root, entry.ID, "SKILL.md"))
		if err != nil {
			continue
		}
		_, _, _, _, version := parseSkillPromptMetadata(string(content))
		code, message := "", ""
		if version == "" {
			code, message = "missing_skill_version", "SKILL.md does not declare a version; package version is not used as a fallback"
		} else if version != entry.Version {
			code, message = "skill_package_version_mismatch", "SKILL.md version differs from the package child version"
		}
		if code != "" {
			entry.Diagnostics = []AdminSkillDiagnostic{skillDiagnostic("warning", code, message, "skills/"+entry.ID+"/SKILL.md")}
		}
	}
	return record
}
