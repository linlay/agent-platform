package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (r *FileRegistry) ReadEditableSkillFile(id string, relPath string) (EditableSkillFileContent, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFileContent{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return EditableSkillFileContent{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	target, cleanRel, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, target); err != nil {
		return EditableSkillFileContent{}, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return EditableSkillFileContent{}, ErrSkillNotFound
	}
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EditableSkillFileContent{}, ErrSkillSymlink
	}
	if info.IsDir() {
		return EditableSkillFileContent{}, ErrSkillIsDirectory
	}
	if info.Size() > EditableSkillMaxTextBytes {
		return EditableSkillFileContent{}, ErrSkillFileTooLarge
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return EditableSkillFileContent{}, err
	}
	if !isEditableSkillText(data) {
		return EditableSkillFileContent{}, ErrSkillFileBinary
	}
	return EditableSkillFileContent{
		ID:        strings.TrimSpace(id),
		Path:      filepath.ToSlash(cleanRel),
		Content:   string(data),
		Encoding:  "utf-8",
		SHA256:    sha256Hex(data),
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UnixMilli(),
	}, nil
}

func (r *FileRegistry) ResolveEditableSkillFile(id string, relPath string) (string, EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return "", EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return "", EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return "", EditableSkillFile{}, err
	}
	target, cleanRel, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return "", EditableSkillFile{}, err
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, target); err != nil {
		return "", EditableSkillFile{}, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", EditableSkillFile{}, ErrSkillNotFound
	}
	if err != nil {
		return "", EditableSkillFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", EditableSkillFile{}, ErrSkillSymlink
	}
	if info.IsDir() {
		return "", EditableSkillFile{}, ErrSkillIsDirectory
	}
	file, err := editableSkillFileMetadataFromInfo(target, cleanRel, info)
	if err != nil {
		return "", EditableSkillFile{}, err
	}
	return target, file, nil
}

func (r *FileRegistry) WriteEditableSkillFile(id string, relPath string, content string, encoding string, baseSHA256 string) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if err := ensureExistingEditableSkillDir(skillDir); err != nil {
		return EditableSkillFile{}, err
	}
	if err := writeEditableSkillTextFile(skillDir, relPath, content, encoding, baseSHA256); err != nil {
		return EditableSkillFile{}, err
	}
	target, cleanRel, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return EditableSkillFile{}, err
	}
	return editableSkillFileMetadata(target, cleanRel)
}

func (r *FileRegistry) DeleteEditableSkillFile(id string, relPath string, recursive bool, baseSHA256 string) error {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return err
	}
	target, _, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, target); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSkillNotFound
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSkillSymlink
	}
	if baseSHA256 != "" {
		if info.IsDir() {
			return ErrSkillIsDirectory
		}
		current, err := sha256File(target)
		if err != nil {
			return err
		}
		if current != strings.TrimSpace(baseSHA256) {
			return ErrSkillConflict
		}
	}
	if info.IsDir() {
		if recursive {
			return os.RemoveAll(target)
		}
		if err := os.Remove(target); err != nil {
			if isDirectoryNotEmptyError(err) {
				return ErrSkillDirectoryNotEmpty
			}
			return err
		}
		return nil
	}
	return os.Remove(target)
}

func (r *FileRegistry) MkdirEditableSkillFile(id string, relPath string) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if err := ensureExistingEditableSkillDir(skillDir); err != nil {
		return EditableSkillFile{}, err
	}
	target, cleanRel, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if isEditableSkillSpecialFile(cleanRel) {
		return EditableSkillFile{}, ErrInvalidSkillPath
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, filepath.Dir(target)); err != nil {
		return EditableSkillFile{}, err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return EditableSkillFile{}, ErrSkillSymlink
		}
		if !info.IsDir() {
			return EditableSkillFile{}, ErrSkillConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return EditableSkillFile{}, err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return EditableSkillFile{}, err
	}
	return editableSkillFileMetadata(target, cleanRel)
}

func (r *FileRegistry) RenameEditableSkillFile(id string, fromPath string, toPath string, overwrite bool) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return EditableSkillFile{}, err
	}
	source, _, err := resolveEditableSkillPath(skillDir, fromPath)
	if err != nil {
		return EditableSkillFile{}, err
	}
	target, cleanTargetRel, err := resolveEditableSkillPath(skillDir, toPath)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, source); err != nil {
		return EditableSkillFile{}, err
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, filepath.Dir(target)); err != nil {
		return EditableSkillFile{}, err
	}
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return EditableSkillFile{}, ErrSkillNotFound
	}
	if err != nil {
		return EditableSkillFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EditableSkillFile{}, ErrSkillSymlink
	}
	if isEditableSkillSpecialFile(cleanTargetRel) && info.IsDir() {
		return EditableSkillFile{}, ErrSkillIsDirectory
	}
	if isEditableSkillSpecialFile(cleanTargetRel) {
		if err := validateEditableSkillSpecialFile(source, cleanTargetRel); err != nil {
			return EditableSkillFile{}, err
		}
	}
	if targetInfo, err := os.Lstat(target); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return EditableSkillFile{}, ErrSkillSymlink
		}
		if !overwrite {
			return EditableSkillFile{}, ErrSkillConflict
		}
		if err := os.RemoveAll(target); err != nil {
			return EditableSkillFile{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return EditableSkillFile{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return EditableSkillFile{}, err
	}
	if err := os.Rename(source, target); err != nil {
		return EditableSkillFile{}, err
	}
	return editableSkillFileMetadata(target, cleanTargetRel)
}

func (r *FileRegistry) UploadEditableSkillFile(id string, relPath string, src io.Reader, overwrite bool) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if err := ensureExistingEditableSkillDir(skillDir); err != nil {
		return EditableSkillFile{}, err
	}
	target, cleanRel, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if err := ensureNoSymlinkAlongExistingPath(skillDir, filepath.Dir(target)); err != nil {
		return EditableSkillFile{}, err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return EditableSkillFile{}, ErrSkillSymlink
		}
		if info.IsDir() {
			return EditableSkillFile{}, ErrSkillIsDirectory
		}
		if !overwrite {
			return EditableSkillFile{}, ErrSkillConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return EditableSkillFile{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return EditableSkillFile{}, err
	}
	tmp := target + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return EditableSkillFile{}, err
	}
	limited := &io.LimitedReader{R: src, N: EditableSkillMaxUploadBytes + 1}
	_, copyErr := io.Copy(out, limited)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return EditableSkillFile{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return EditableSkillFile{}, closeErr
	}
	if limited.N <= 0 {
		_ = os.Remove(tmp)
		return EditableSkillFile{}, ErrSkillFileTooLarge
	}
	if err := validateEditableSkillSpecialFile(tmp, cleanRel); err != nil {
		_ = os.Remove(tmp)
		return EditableSkillFile{}, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return EditableSkillFile{}, err
	}
	return editableSkillFileMetadata(target, cleanRel)
}

func writeEditableSkillTextFile(skillDir string, relPath string, content string, encoding string, baseSHA256 string) error {
	encoding = strings.ToLower(strings.TrimSpace(encoding))
	if encoding == "" {
		encoding = "utf-8"
	}
	if encoding != "utf-8" {
		return ErrSkillUnsupportedEncoding
	}
	if int64(len([]byte(content))) > EditableSkillMaxTextBytes {
		return ErrSkillFileTooLarge
	}
	if !utf8.ValidString(content) {
		return ErrSkillFileBinary
	}
	_, cleanRel, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return err
	}
	if filepath.ToSlash(cleanRel) == "SKILL.md" && strings.TrimSpace(content) == "" {
		return fmt.Errorf("SKILL.md is required")
	}
	if filepath.ToSlash(cleanRel) == ".runtime-env.json" {
		var env map[string]string
		if err := json.Unmarshal([]byte(content), &env); err != nil {
			return fmt.Errorf(".runtime-env.json must be a JSON object with string values")
		}
	}
	target := filepath.Join(skillDir, cleanRel)
	if err := ensureNoSymlinkAlongExistingPath(skillDir, filepath.Dir(target)); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSkillSymlink
		}
		if info.IsDir() {
			return ErrSkillIsDirectory
		}
		if baseSHA256 != "" {
			current, err := sha256File(target)
			if err != nil {
				return err
			}
			if current != strings.TrimSpace(baseSHA256) {
				return ErrSkillConflict
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if baseSHA256 != "" {
		return ErrSkillConflict
	}
	return writeFileAtomic(target, []byte(content), 0o644)
}

func validateEditableSkillSpecialFile(pathOnDisk string, relPath string) error {
	switch filepath.ToSlash(relPath) {
	case "SKILL.md":
		content, err := os.ReadFile(pathOnDisk)
		if err != nil {
			return err
		}
		if !isEditableSkillText(content) {
			return ErrSkillFileBinary
		}
		if strings.TrimSpace(string(content)) == "" {
			return fmt.Errorf("SKILL.md is required")
		}
	case ".runtime-env.json":
		content, err := os.ReadFile(pathOnDisk)
		if err != nil {
			return err
		}
		if !isEditableSkillText(content) {
			return ErrSkillFileBinary
		}
		var env map[string]string
		if err := json.Unmarshal(content, &env); err != nil {
			return fmt.Errorf(".runtime-env.json must be a JSON object with string values")
		}
	}
	return nil
}

func isEditableSkillSpecialFile(relPath string) bool {
	switch filepath.ToSlash(relPath) {
	case "SKILL.md", ".runtime-env.json":
		return true
	default:
		return false
	}
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readSmallFilePrefix(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: max}
	return io.ReadAll(limited)
}

func isEditableSkillText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	return !bytes.Contains(data, []byte{0})
}

func isDirectoryNotEmptyError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "directory not empty") || strings.Contains(text, "not empty")
}
