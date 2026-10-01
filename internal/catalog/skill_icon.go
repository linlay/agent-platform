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
func skillIconPath(skillDir, id string) string {
	rel, err := resolveAdminSkillIcon(skillDir, id)
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
	return readSkillIconFile(filepath.Dir(filepath.Dir(skill.IconPath)), filepath.Join("assets", filepath.Base(skill.IconPath)))
}

func readSkillIconFile(root, relative string) ([]byte, string, error) {
	iconPath := filepath.Join(root, relative)
	if err := ensureNoSymlinkAlongExistingPath(root, iconPath); err != nil {
		return nil, "", err
	}
	file, err := os.OpenInRoot(root, relative)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	isSVG := strings.EqualFold(filepath.Ext(iconPath), ".svg")
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

// Package icons belong to the package root, not a member's assets directory.
func skillPackageIconName(root string) string {
	for _, name := range []string{"icon.svg", "icon.png"} {
		full := filepath.Join(root, name)
		if ensureNoSymlinkAlongExistingPath(root, full) != nil {
			continue
		}
		if info, err := os.Lstat(full); err == nil && info.Mode().IsRegular() {
			return name
		}
	}
	return ""
}

func (r *FileRegistry) ReadSkillPackageIcon(id string) ([]byte, string, error) {
	if err := ValidateSkillPackageID(id); err != nil {
		return nil, "", err
	}
	r.skillPackageMu.Lock()
	defer r.skillPackageMu.Unlock()
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, "", os.ErrNotExist
	}
	_, _, exists, err := readSkillPackageRecord(root, id)
	if err != nil {
		return nil, "", err
	}
	if !exists {
		return nil, "", os.ErrNotExist
	}
	dir := filepath.Join(root, id)
	name := skillPackageIconName(dir)
	if name == "" {
		return nil, "", os.ErrNotExist
	}
	return readSkillIconFile(dir, name)
}
