package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/skillmeta"
)

// SkillPackageMember identifies one direct child; presentation stays in SKILL.md.
type SkillPackageMember struct {
	ID    string `json:"id"`
	extra map[string]json.RawMessage
}

// SkillPackageMetadata owns the ordered package membership list.
type SkillPackageMetadata struct {
	Name        string               `json:"name"`
	Skills      []SkillPackageMember `json:"skills"`
	DisplayName string               `json:"displayName,omitempty"`
	Description string               `json:"description,omitempty"`
	Version     string               `json:"version,omitempty"`
	Triggers    []string             `json:"triggers,omitempty"`
	Metadata    map[string]any       `json:"metadata,omitempty"`
	extra       map[string]json.RawMessage
}

// Preserve author extensions when canonicalizing manifests. Unknown properties
// never participate in membership or display metadata resolution.
func (m *SkillPackageMember) UnmarshalJSON(data []byte) error {
	type plain SkillPackageMember
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(data, &extra); err != nil {
		return err
	}
	// Installed packages may still use the former member key. Always write id.
	if legacy, ok := extra["key"]; ok {
		var id string
		if err := json.Unmarshal(legacy, &id); err != nil {
			return err
		}
		if _, present := extra["id"]; !present {
			value.ID = id
		} else if value.ID != id {
			return fmt.Errorf("skill member id and legacy key disagree")
		}
	}
	delete(extra, "key")
	delete(extra, "id")
	*m = SkillPackageMember(value)
	if len(extra) > 0 {
		m.extra = extra
	}
	return nil
}
func (m SkillPackageMember) MarshalJSON() ([]byte, error) {
	type plain SkillPackageMember
	return marshalSkillPackageExtensions(plain(m), m.extra)
}
func (m *SkillPackageMetadata) UnmarshalJSON(data []byte) error {
	type plain SkillPackageMetadata
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(data, &extra); err != nil {
		return err
	}
	for _, key := range []string{"name", "skills", "displayName", "description", "version", "triggers", "metadata"} {
		delete(extra, key)
	}
	*m = SkillPackageMetadata(value)
	if len(extra) > 0 {
		m.extra = extra
	}
	return nil
}
func (m SkillPackageMetadata) MarshalJSON() ([]byte, error) {
	type plain SkillPackageMetadata
	return marshalSkillPackageExtensions(plain(m), m.extra)
}
func marshalSkillPackageExtensions(value any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range extra {
		fields[key] = value
	}
	return json.Marshal(fields)
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
	return ValidateEditableSkillID(id)
}

func parseSkillPackageMetadata(content []byte) (SkillPackageMetadata, error) {
	var m SkillPackageMetadata
	if len(content) > 1<<20 || json.Unmarshal(content, &m) != nil || ValidateSkillPackageID(m.Name) != nil {
		return m, skillArchiveValidationError("invalid_package_manifest", "package.json requires a valid name and valid JSON", "package.json")
	}
	if m.Skills == nil {
		return m, skillArchiveValidationError("invalid_package_manifest", "package.json requires a skills object array (use [] for an empty package)", "package.json")
	}
	seen := map[string]bool{}
	for _, member := range m.Skills {
		folded := strings.ToLower(member.ID)
		if ValidateSkillPackageID(member.ID) != nil || seen[folded] {
			return m, skillArchiveValidationError("invalid_package_manifest", "skills must contain unique, safe package-relative IDs", "package.json")
		}
		seen[folded] = true
	}
	return m, nil
}

func (m SkillPackageMetadata) hasMember(key string) bool {
	for _, member := range m.Skills {
		if member.ID == key {
			return true
		}
	}
	return false
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
	record := SkillPackageRecord{HasIcon: skillPackageIconName(dir) != "", ID: id, Name: m.Name, DisplayName: m.DisplayName, Description: m.Description, Version: version, Triggers: m.Triggers, Metadata: metadata, Presentation: skillmeta.Parse(metadata, version), Skills: []SkillPackageRecordSkill{}, SchemaVersion: 1}
	for _, member := range m.Skills {
		child := SkillPackageRecordSkill{ID: id + "/" + member.ID, Name: member.ID, Path: "./" + member.ID}
		childRoot := filepath.Join(dir, member.ID)
		data, err := readDeclaredSkillMember(childRoot)
		if err != nil {
			code := "invalid_package_member"
			if errors.Is(err, os.ErrNotExist) {
				code = "missing_package_skill"
			}
			child.Diagnostics = []AdminSkillDiagnostic{skillDiagnostic("error", code, err.Error(), member.ID+"/SKILL.md")}
			record.Skills = append(record.Skills, child)
			continue
		}
		name, description, triggers, meta, version := parseSkillPromptMetadata(string(data))
		if name != "" {
			child.Name = name
		}
		child.Description, child.Triggers, child.Metadata, child.Version = description, triggers, meta, version
		child.DisplayName = skillmeta.String(meta["displayName"])
		record.Skills = append(record.Skills, child)
	}
	return record, nil
}

// readDeclaredSkillMember rejects symlink roots before reading any member content.
func readDeclaredSkillMember(root string) ([]byte, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillSymlink
	}
	if !info.IsDir() {
		return nil, ErrInvalidSkillPath
	}
	path := filepath.Join(root, "SKILL.md")
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillSymlink
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalidSkillPath
	}
	if info.Size() > EditableSkillMaxTextBytes {
		return nil, ErrSkillFileTooLarge
	}
	return os.ReadFile(path)
}

func validateDeclaredSkillMembers(root string, m SkillPackageMetadata, allowMissing bool) error {
	for _, member := range m.Skills {
		child := filepath.Join(root, member.ID)
		if _, err := readDeclaredSkillMember(child); err != nil {
			if allowMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return skillArchiveValidationError("invalid_package_member", err.Error(), member.ID+"/SKILL.md")
		}
		if err := validateImportedEditableSkill(child); err != nil {
			return err
		}
	}
	return nil
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
		m = SkillPackageMetadata{Name: id, DisplayName: legacy.Name, Version: legacy.Version, Skills: []SkillPackageMember{}}
		for _, child := range children {
			m.Skills = append(m.Skills, SkillPackageMember{ID: child.ID})
			if err := os.Rename(child.Root, filepath.Join(candidate, child.ID)); err != nil {
				return m, nil, "", err
			}
		}
		sort.Slice(m.Skills, func(i, j int) bool { return m.Skills[i].ID < m.Skills[j].ID })
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
	if err := validateDeclaredSkillMembers(candidate, m, false); err != nil {
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

func (r *FileRegistry) ReadEditableSkillPackageManifest(id string) (EditableSkillFileContent, error) {
	if r == nil {
		return EditableSkillFileContent{}, ErrSkillPackageNotFound
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	_, data, exists, err := readSkillPackageRecord(root, id)
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	if !exists {
		return EditableSkillFileContent{}, ErrSkillPackageNotFound
	}
	info, err := os.Stat(filepath.Join(root, id, "package.json"))
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	return EditableSkillFileContent{ID: id, Path: "package.json", Content: string(data), Encoding: "utf-8", SHA256: sha256Hex(data), Size: int64(len(data)), UpdatedAt: info.ModTime().UnixMilli()}, nil
}
func (r *FileRegistry) BeginUpdateEditableSkillPackageManifest(id, content, baseSHA256 string) (*EditableSkillPackageMutation, SkillPackageRecord, error) {
	if r == nil {
		return nil, SkillPackageRecord{}, ErrSkillPackageNotFound
	}
	m, err := parseSkillPackageMetadata([]byte(content))
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if m.Name != id {
		return nil, SkillPackageRecord{}, fmt.Errorf("%w: package name cannot be changed", ErrSkillPackageConflict)
	}
	r.skillPackageMu.Lock()
	owned := false
	defer func() {
		if !owned {
			r.skillPackageMu.Unlock()
		}
	}()
	current, err := r.ReadEditableSkillPackageManifest(id)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	if baseSHA256 == "" || baseSHA256 != current.SHA256 {
		return nil, SkillPackageRecord{}, ErrSkillConflict
	}
	previous, err := parseSkillPackageMetadata([]byte(current.Content))
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	usage := r.skillUsageByAgent()
	for _, member := range previous.Skills {
		if !m.hasMember(member.ID) && len(usage[id+"/"+member.ID]) > 0 {
			return nil, SkillPackageRecord{}, fmt.Errorf("%w: skill %s/%s is used by agents", ErrSkillPackageConflict, id, member.ID)
		}
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if err := validateDeclaredSkillMembers(filepath.Join(root, id), m, true); err != nil {
		return nil, SkillPackageRecord{}, err
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), skillPackageBackupPrefix)
	if err != nil {
		return nil, SkillPackageRecord{}, err
	}
	mutation := &EditableSkillPackageMutation{root: root, recordPath: filepath.Join(root, id, "package.json"), oldRecord: []byte(current.Content), oldRecordExists: true, backupRoot: backup, unlock: r.skillPackageMu.Unlock}
	owned = true
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		err = writeSkillPackageRecordFile(mutation.recordPath, append(encoded, '\n'))
	}
	if err != nil {
		return nil, SkillPackageRecord{}, errors.Join(err, mutation.Rollback())
	}
	mutation.recordChanged = true
	record, err := ScanSkillPackageRecord(root, id)
	if err != nil {
		return nil, SkillPackageRecord{}, errors.Join(err, mutation.Rollback())
	}
	return mutation, record, nil
}
