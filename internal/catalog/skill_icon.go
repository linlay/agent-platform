package catalog

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Skill icons use the same assets/<key>.png convention as the management UI.
func skillIconPath(skillDir, key string) string {
	rel, err := resolveAdminSkillIcon(skillDir, key)
	if err != nil || rel == "" {
		return ""
	}
	full := filepath.Join(skillDir, filepath.FromSlash(rel))
	if ensureNoSymlinkAlongExistingPath(skillDir, full) != nil {
		return ""
	}
	return full
}

// ReadSkillIcon keeps the read inside the resolved skill's assets directory,
// even if a package is replaced between catalog loading and this request.
func ReadSkillIcon(skill SkillDefinition) ([]byte, error) {
	if skill.IconPath == "" {
		return nil, os.ErrNotExist
	}
	root := filepath.Dir(filepath.Dir(skill.IconPath))
	if err := ensureNoSymlinkAlongExistingPath(root, skill.IconPath); err != nil {
		return nil, err
	}
	file, err := os.OpenInRoot(root, filepath.Join("assets", filepath.Base(skill.IconPath)))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > EditableSkillMaxUploadBytes {
		return nil, os.ErrNotExist
	}
	data, err := io.ReadAll(io.LimitReader(file, EditableSkillMaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > EditableSkillMaxUploadBytes || http.DetectContentType(data) != "image/png" {
		return nil, os.ErrNotExist
	}
	return data, nil
}
