package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/skillmeta"
)

// SkillPackageMetadata describes only the package. Members are discovered from
// direct child directories, never from a second, potentially stale manifest list.
type SkillPackageMetadata struct {
	Name        string         `json:"name"`
	DisplayName string         `json:"displayName,omitempty"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version,omitempty"`
	Triggers    []string       `json:"triggers,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Shared directory eligibility for package lists, skill lists and ownership.
// Reserved connector skills and example/hidden directories are not user skills.
func isSkillCenterDirectory(entry os.DirEntry) bool {
	return entry.IsDir() && ValidateSkillPackageID(entry.Name()) == nil
}

func logInvalidSkillPackage(root, name string, err error) {
	log.Printf("[catalog][skills] warning code=invalid_skill_package package=%q source=%q error=%v; skipping package", name, filepath.Join(root, name, "package.json"), err)
}

func ValidateSkillPackageID(id string) error {
	if id != strings.TrimSpace(id) || strings.ContainsAny(id, "/\\") {
		return ErrInvalidSkillPath
	}
	return ValidateEditableSkillKey(id)
}

func parseSkillPackageMetadata(content []byte) (SkillPackageMetadata, error) {
	var m SkillPackageMetadata
	if len(content) > 1<<20 || json.Unmarshal(content, &m) != nil || ValidateSkillPackageID(m.Name) != nil {
		return m, skillArchiveValidationError("invalid_package_manifest", "package.json requires a valid name and valid JSON", "package.json")
	}
	return m, nil
}

func ReadSkillPackageManifest(root string) (SkillPackageMetadata, error) {
	if info, err := os.Lstat(root); err != nil {
		return SkillPackageMetadata{}, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return SkillPackageMetadata{}, ErrSkillSymlink
	} else if !info.IsDir() {
		return SkillPackageMetadata{}, ErrInvalidSkillPath
	}
	path := filepath.Join(root, "package.json")
	info, err := os.Lstat(path)
	if err != nil {
		return SkillPackageMetadata{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return SkillPackageMetadata{}, ErrSkillSymlink
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return SkillPackageMetadata{}, ErrSkillFileTooLarge
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return SkillPackageMetadata{}, err
	}
	return parseSkillPackageMetadata(content)
}

func ensureSkillPackageRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return ErrInvalidSkillPath
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSkillSymlink
	}
	if !info.IsDir() {
		return ErrInvalidSkillPath
	}
	return nil
}

func ScanSkillPackageRecord(root, id string) (SkillPackageRecord, error) {
	if err := ValidateSkillPackageID(id); err != nil {
		return SkillPackageRecord{}, err
	}
	if info, err := os.Lstat(root); err != nil {
		return SkillPackageRecord{}, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return SkillPackageRecord{}, ErrSkillSymlink
	}
	return scanPackageAt(filepath.Join(root, id), id)
}
func scanPackageAt(dir, id string) (SkillPackageRecord, error) {
	m, err := ReadSkillPackageManifest(dir)
	if err != nil {
		return SkillPackageRecord{}, err
	}
	if m.Name != id {
		return SkillPackageRecord{}, fmt.Errorf("%w: package name differs from directory", ErrInvalidSkillPath)
	}
	metadata := map[string]any{}
	for k, v := range m.Metadata {
		metadata[k] = v
	}
	if m.DisplayName != "" {
		metadata["displayName"] = m.DisplayName
	}
	version := strings.TrimSpace(m.Version)
	if version == "" {
		version = skillmeta.String(metadata["version"])
	}
	record := SkillPackageRecord{ID: id, Name: m.Name, DisplayName: m.DisplayName, Description: m.Description, Version: version, Triggers: m.Triggers, Metadata: metadata, Presentation: skillmeta.Parse(metadata, version), Skills: []SkillPackageRecordSkill{}, SchemaVersion: 1}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return record, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return record, ErrSkillSymlink
		}
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		path := filepath.Join(child, "SKILL.md")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return record, err
		}
		if !info.Mode().IsRegular() {
			return record, ErrSkillSymlink
		}
		if info.Size() > EditableSkillMaxTextBytes {
			return record, ErrSkillFileTooLarge
		}
		if err := ValidateSkillPackageID(entry.Name()); err != nil {
			return record, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return record, err
		}
		name, description, triggers, meta, version := parseSkillPromptMetadata(string(data))
		if name == "" {
			name = entry.Name()
		}
		record.Skills = append(record.Skills, SkillPackageRecordSkill{ID: id + "/" + entry.Name(), Name: name, Path: "./" + entry.Name(), DisplayName: skillmeta.String(meta["displayName"]), Description: description, Version: version, Triggers: triggers, Metadata: meta})
	}
	return record, nil
}
func readSkillPackageRecord(root, id string) (SkillPackageRecord, []byte, bool, error) {
	if err := ValidateSkillPackageID(id); err != nil {
		return SkillPackageRecord{}, nil, false, err
	}
	// A normal standalone skill may own package.json for npm; SKILL.md wins.
	if _, err := os.Lstat(filepath.Join(root, id, "SKILL.md")); err == nil {
		return SkillPackageRecord{}, nil, false, nil
	}
	record, err := ScanSkillPackageRecord(root, id)
	if errors.Is(err, os.ErrNotExist) {
		return SkillPackageRecord{}, nil, false, nil
	}
	if err != nil {
		return record, nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(root, id, "package.json"))
	return record, data, true, err
}

func prepareNestedSkillPackage(stage, id, version string) (SkillPackageMetadata, []preparedPackageSkill, string, error) {
	candidate := stage
	if _, err := os.Stat(filepath.Join(stage, "SKILL.md")); err == nil {
		return SkillPackageMetadata{}, nil, "", skillArchiveValidationError("invalid_package_layout", "skill package cannot have a root SKILL.md", "SKILL.md")
	}
	m, err := ReadSkillPackageManifest(stage)
	if errors.Is(err, os.ErrNotExist) {
		legacy, children, legacyErr := validatePreparedSkillPackage(stage, id, version)
		if legacyErr != nil {
			return m, nil, "", legacyErr
		}
		candidate = filepath.Join(stage, ".canonical-package")
		if err := os.Mkdir(candidate, 0o755); err != nil {
			return m, nil, "", err
		}
		m = SkillPackageMetadata{Name: id, DisplayName: legacy.Name, Version: legacy.Version}
		for _, child := range children {
			if err := os.Rename(child.Root, filepath.Join(candidate, child.ID)); err != nil {
				return m, nil, "", err
			}
		}
	} else if err != nil {
		return m, nil, "", err
	}
	if m.Name != id {
		return m, nil, "", skillArchiveValidationError("invalid_package_manifest", "package name does not match requested identity", "package.json")
	}
	// Version is optional; when supplied in the package it must agree with an
	// explicit market request. Missing versions are not manufactured.
	declared := m.Version
	if declared == "" {
		declared = skillmeta.String(m.Metadata["version"])
	}
	if version != "" && declared != "" && version != declared {
		return m, nil, "", ErrSkillArchiveInvalid
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, nil, "", err
	}
	if err = os.WriteFile(filepath.Join(candidate, "package.json"), append(encoded, '\n'), 0o644); err != nil {
		return m, nil, "", err
	}
	record, err := scanPackageAt(candidate, id)
	if err != nil {
		return m, nil, "", err
	}
	prepared := []preparedPackageSkill{}
	for _, child := range record.Skills {
		childRoot := filepath.Join(candidate, strings.TrimPrefix(child.Path, "./"))
		if err := validateImportedEditableSkill(childRoot); err != nil {
			return m, nil, "", err
		}
		prepared = append(prepared, preparedPackageSkill{ID: child.ID, Version: child.Version, Root: childRoot})
	}
	return m, prepared, candidate, nil
}

func (r *FileRegistry) ReadEditableSkillPackageManifest(key string) (EditableSkillFileContent, error) {
	if r == nil {
		return EditableSkillFileContent{}, ErrSkillPackageNotFound
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	_, data, exists, err := readSkillPackageRecord(root, key)
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	if !exists {
		return EditableSkillFileContent{}, ErrSkillPackageNotFound
	}
	info, err := os.Stat(filepath.Join(root, key, "package.json"))
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	return EditableSkillFileContent{Key: key, Path: "package.json", Content: string(data), Encoding: "utf-8", SHA256: sha256Hex(data), Size: int64(len(data)), UpdatedAt: info.ModTime().UnixMilli()}, nil
}
func (r *FileRegistry) BeginUpdateEditableSkillPackageManifest(key, content, baseSHA256 string) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, ErrSkillPackageNotFound
	}
	m, err := parseSkillPackageMetadata([]byte(content))
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if m.Name != key {
		return nil, SkillPackageRecord{}, fmt.Errorf("%w: package name cannot be changed", ErrSkillPackageConflict)
	}
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	current, err := r.ReadEditableSkillPackageManifest(key)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if baseSHA256 == "" || baseSHA256 != current.SHA256 {
		return nil, SkillPackageRecord{}, ErrSkillConflict
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	backup, err := os.MkdirTemp(filepath.Dir(root), skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	mutation := &EditableSkillPackageMutation{root: root, recordPath: filepath.Join(root, key, "package.json"), oldRecord: []byte(current.Content), oldRecordExists: true, backupRoot: backup, unlock: r.skillPackageMu.Unlock}
	owned = true
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		err = writeSkillPackageRecordFile(mutation.recordPath, append(encoded, '\n'))
	}
	if err != nil {
		return nil, SkillPackageRecord{}, errors.Join(err, mutation.Rollback())
	}
	mutation.recordChanged = true
	record, err := ScanSkillPackageRecord(root, key)
	if err != nil {
		return nil, SkillPackageRecord{}, errors.Join(err, mutation.Rollback())
	}
	return mutation, record, nil
}
