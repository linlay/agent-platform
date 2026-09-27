package catalog

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

// Prefer assets/icon.svg or icon.png, while accepting legacy key-based names.
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
func ReadSkillIcon(skill SkillDefinition) ([]byte, string, error) {
	if skill.IconPath == "" {
		return nil, "", os.ErrNotExist
	}
	root := filepath.Dir(filepath.Dir(skill.IconPath))
	if err := ensureNoSymlinkAlongExistingPath(root, skill.IconPath); err != nil {
		return nil, "", err
	}
	file, err := os.OpenInRoot(root, filepath.Join("assets", filepath.Base(skill.IconPath)))
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	isSVG := strings.EqualFold(filepath.Ext(skill.IconPath), ".svg")
	limit := int64(EditableSkillMaxUploadBytes)
	if isSVG {
		limit = connector.MaxIconBytes
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, "", os.ErrNotExist
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 || int64(len(data)) > limit {
		return nil, "", os.ErrNotExist
	}
	if isSVG {
		if connector.ValidateIconSVG(data) != nil {
			return nil, "", os.ErrNotExist
		}
		return data, "image/svg+xml", nil
	}
	if http.DetectContentType(data) != "image/png" {
		return nil, "", os.ErrNotExist
	}
	return data, "image/png", nil
}
