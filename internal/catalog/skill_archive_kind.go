package catalog

import (
	"archive/zip"
	"encoding/json"
	"io"
	"strings"
)

// DetectSkillPackageArchive reads only the bounded root manifest. Both import
// paths still validate and safely extract the complete archive before publishing.
// A regular skill can contain an unrelated manifest.json alongside SKILL.md.
func DetectSkillPackageArchive(source io.ReaderAt, size int64) (packageID, version string, isPackage bool, err error) {
	if source == nil || size <= 0 {
		return "", "", false, ErrSkillArchiveInvalid
	}
	if size > EditableSkillPackageMaxUploadBytes {
		return "", "", false, ErrSkillArchiveUploadTooLarge
	}
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return "", "", false, ErrSkillArchiveInvalid
	}
	entries, err := planEditableSkillPackageArchive(reader.File)
	if err != nil {
		return "", "", false, err
	}
	var manifestFile *zip.File
	hasRootSkill := false
	for _, entry := range entries {
		if !entry.dir && entry.path == "manifest.json" {
			manifestFile = entry.file
		}
		if !entry.dir && entry.path == "SKILL.md" {
			hasRootSkill = true
		}
	}
	if manifestFile == nil {
		return "", "", false, nil
	}
	input, err := manifestFile.Open()
	if err != nil {
		return "", "", false, skillArchiveValidationError("invalid_package_manifest", "manifest.json cannot be opened", "manifest.json")
	}
	defer input.Close()
	content, err := io.ReadAll(io.LimitReader(input, EditableSkillMaxTextBytes+1))
	if err != nil || int64(len(content)) > EditableSkillMaxTextBytes {
		return "", "", false, skillArchiveValidationError("invalid_package_manifest", "manifest.json cannot be read or exceeds the maximum text size", "manifest.json")
	}
	var manifest skillPackageManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return "", "", false, skillArchiveValidationError("invalid_package_manifest", "manifest.json must be valid JSON", "manifest.json")
	}
	if strings.TrimSpace(manifest.Type) != "skill-package" {
		if hasRootSkill {
			return "", "", false, nil
		}
		return "", "", false, skillArchiveValidationError("invalid_package_manifest", "manifest.json type must be skill-package", "manifest.json")
	}
	packageID, version = strings.TrimSpace(manifest.ID), strings.TrimSpace(manifest.Version)
	if manifest.SchemaVersion != 1 || ValidateEditableSkillKey(packageID) != nil || version == "" || len(manifest.Skills) == 0 {
		return "", "", false, skillArchiveValidationError("invalid_package_manifest", "manifest.json must declare schemaVersion 1, a valid id, a version and skills", "manifest.json")
	}
	return packageID, version, true, nil
}
