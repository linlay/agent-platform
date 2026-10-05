package catalog

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/connector"
)

// validSkillPathID accepts a standalone ID or one package/member pair.
// It is also used for runtime connector skill IDs, where reserved names remain valid.
func validSkillPathID(id string) bool {
	if strings.TrimSpace(id) != id {
		return false
	}
	parts := strings.Split(id, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !validRuntimeComponent(part) || strings.HasPrefix(part, ".") || !ShouldLoadRuntimeName(part) {
			return false
		}
	}
	return true
}

func ValidateEditableSkillID(id string) error {
	id = strings.TrimSpace(id)
	if !validSkillPathID(id) {
		return ErrInvalidSkillID
	}
	for _, part := range strings.Split(id, "/") {
		if connector.IsReservedSkill(part) {
			return fmt.Errorf("%w: connector skills belong to their connector package", ErrInvalidSkillID)
		}
	}
	return nil
}

func ensureExistingEditableSkillDir(skillDir string) error {
	info, err := os.Lstat(skillDir)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSkillNotFound
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSkillSymlink
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: skill root is not a directory", ErrInvalidSkillPath)
	}
	return nil
}

func editableSkillDir(root string, id string) (string, error) {
	if err := ValidateEditableSkillID(id); err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	dir := filepath.Join(root, filepath.FromSlash(id))
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", ErrSkillSymlink
	}
	if err := ensureNoSymlinkAlongExistingPath(root, dir); err != nil {
		return "", err
	}
	if strings.Contains(id, "/") {
		parent := filepath.Dir(dir)
		if _, err := os.Lstat(filepath.Join(parent, "SKILL.md")); err == nil {
			return "", ErrInvalidSkillPath
		}
		manifest, err := ReadSkillPackageManifest(parent)
		if err != nil {
			return "", fmt.Errorf("%w: invalid parent package: %v", ErrInvalidSkillPath, err)
		}
		if !manifest.hasMember(filepath.Base(dir)) {
			return "", ErrSkillNotFound
		}
		if manifest.Name != strings.SplitN(id, "/", 2)[0] {
			return "", fmt.Errorf("%w: package name differs from directory", ErrInvalidSkillPath)
		}
	} else if _, err := os.Lstat(filepath.Join(dir, "SKILL.md")); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(dir, "package.json")); err == nil {
			return "", fmt.Errorf("%w: use the skill package endpoint", ErrInvalidSkillPath)
		}
	}
	if !insideDir(root, dir) {
		return "", ErrInvalidSkillPath
	}
	return dir, nil
}

func resolveEditableSkillPath(skillDir string, relPath string) (string, string, error) {
	clean, err := validateEditableSkillRelativePath(relPath)
	if err != nil {
		return "", "", err
	}
	target := filepath.Join(skillDir, clean)
	if !insideDir(skillDir, target) {
		return "", "", ErrInvalidSkillPath
	}
	return target, clean, nil
}

func validateEditableSkillRelativePath(relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalidSkillPath)
	}
	if strings.Contains(relPath, `\`) || strings.Contains(relPath, "\x00") || path.IsAbs(relPath) || filepath.IsAbs(relPath) {
		return "", ErrInvalidSkillPath
	}
	clean := path.Clean(relPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrInvalidSkillPath
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrInvalidSkillPath
		}
	}
	return filepath.FromSlash(clean), nil
}

func ensureNoSymlinkAlongExistingPath(root string, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == "." {
		return err
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return ErrInvalidSkillPath
	}
	current := rootAbs
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSkillSymlink
		}
	}
	return nil
}
