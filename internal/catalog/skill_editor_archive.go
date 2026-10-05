package catalog

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteEditableSkillArchive writes a portable ZIP archive of a skill's safe,
// distributable files. Runtime environment configuration is intentionally
// omitted because it may contain credentials.
func (r *FileRegistry) WriteEditableSkillArchive(id string, destination io.Writer) error {
	if r == nil {
		return fmt.Errorf("skill registry is not configured")
	}
	if destination == nil {
		return fmt.Errorf("skill archive destination is required")
	}
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

	files, err := editableSkillArchiveFiles(skillDir)
	if err != nil {
		return err
	}
	archiveRoot, err := os.OpenRoot(skillDir)
	if err != nil {
		return err
	}
	defer archiveRoot.Close()
	rootInfo, err := archiveRoot.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, rootInfo) {
		return fmt.Errorf("%w: skill root changed while building archive", ErrSkillConflict)
	}
	archive := zip.NewWriter(destination)
	for _, file := range files {
		if err := writeEditableSkillArchiveFile(archive, archiveRoot, file); err != nil {
			_ = archive.Close()
			return err
		}
	}
	return archive.Close()
}

// ImportEditableSkillArchive validates and atomically installs a ZIP archive
// into the shared skills center. The archive may either contain SKILL.md at
// its root or wrap the complete skill in one top-level directory.
func (r *FileRegistry) ImportEditableSkillArchive(id string, source io.ReaderAt, size int64) (AdminSkill, error) {
	mutation, item, err := r.BeginImportEditableSkillArchive(id, source, size, false)
	if err != nil {
		return AdminSkill{}, err
	}
	return item, mutation.Commit()
}

// importEditableSkillArchiveIntoRoot contains the shared safe ZIP extraction
// path for center and Agent-private skills. The caller owns the returned
// directory and must remove it when a larger mutation subsequently fails.
func importEditableSkillArchiveIntoRoot(root string, id string, source io.ReaderAt, size int64) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("skill root is required")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if source == nil || size <= 0 {
		return "", ErrSkillArchiveInvalid
	}
	if size > EditableSkillMaxUploadBytes {
		return "", ErrSkillArchiveUploadTooLarge
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", ErrSkillSymlink
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: skill root is not a directory", ErrInvalidSkillPath)
	}
	finalDir, err := editableSkillDir(root, id)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(finalDir); err == nil {
		return "", ErrSkillAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	reader, err := zip.NewReader(source, size)
	if err != nil {
		return "", ErrSkillArchiveInvalid
	}
	entries, err := planEditableSkillArchiveImport(reader.File)
	if err != nil {
		return "", err
	}
	stagingDir, err := os.MkdirTemp(root, editableSkillImportStagingPrefix)
	if err != nil {
		return "", err
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	if err := os.Chmod(stagingDir, 0o755); err != nil {
		return "", err
	}
	var extractedBytes int64
	for _, entry := range entries {
		written, err := extractEditableSkillArchiveEntry(stagingDir, entry, EditableSkillMaxArchiveBytes-extractedBytes)
		if err != nil {
			return "", err
		}
		extractedBytes += written
	}
	if err := validateImportedEditableSkill(stagingDir); err != nil {
		return "", err
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		if _, statErr := os.Lstat(finalDir); statErr == nil {
			return "", ErrSkillAlreadyExists
		}
		return "", err
	}
	removeStaging = false
	return finalDir, nil
}

// DetectEditableSkillArchiveID returns the package-defined skill ID without
// extracting it. A package may explicitly set frontmatter.id (legacy key is accepted); otherwise its
// required frontmatter.name is the stable ID. This lets Agent-private imports
// preserve the identity supplied by the ZIP itself.
func DetectEditableSkillArchiveID(source io.ReaderAt, size int64) (string, error) {
	if source == nil || size <= 0 {
		return "", ErrSkillArchiveInvalid
	}
	if size > EditableSkillMaxUploadBytes {
		return "", ErrSkillArchiveUploadTooLarge
	}
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return "", ErrSkillArchiveInvalid
	}
	entries, err := planEditableSkillArchiveImport(reader.File)
	if err != nil {
		return "", err
	}
	var skillFile *zip.File
	for _, entry := range entries {
		if !entry.dir && entry.path == "SKILL.md" {
			skillFile = entry.file
			break
		}
	}
	if skillFile == nil {
		return "", skillArchiveValidationError("missing_skill_md", "SKILL.md is required", "SKILL.md")
	}
	input, err := skillFile.Open()
	if err != nil {
		return "", skillArchiveValidationError("corrupt_entry", "SKILL.md cannot be opened", "SKILL.md")
	}
	defer input.Close()
	content, err := io.ReadAll(io.LimitReader(input, EditableSkillMaxTextBytes+1))
	if err != nil {
		return "", skillArchiveValidationError("corrupt_entry", "SKILL.md cannot be read", "SKILL.md")
	}
	if int64(len(content)) > EditableSkillMaxTextBytes {
		return "", skillArchiveValidationError("skill_md_too_large", "SKILL.md exceeds the maximum text size", "SKILL.md")
	}
	frontmatter, _ := parseSkillFrontMatter(strings.ReplaceAll(string(content), "\r\n", "\n"))
	key := strings.TrimSpace(frontMatterString(frontmatter["id"]))
	legacyID := strings.TrimSpace(frontMatterString(frontmatter["key"]))
	if key != "" && legacyID != "" && key != legacyID {
		return "", skillArchiveValidationError("invalid_skill_id", "SKILL.md id and legacy key disagree", "SKILL.md")
	}
	if key == "" {
		key = legacyID
	}
	if key == "" {
		key = strings.TrimSpace(frontMatterString(frontmatter["name"]))
	}
	if key == "" {
		return "", skillArchiveValidationError("missing_skill_name", "SKILL.md frontmatter.name is required to derive the skill ID", "SKILL.md")
	}
	if err := ValidateEditableSkillID(key); err != nil || strings.Contains(key, "/") {
		return "", skillArchiveValidationError("invalid_skill_id", "SKILL.md frontmatter.id, legacy key or name must be a valid skill ID", "SKILL.md")
	}
	return key, nil
}

type editableSkillArchiveImportEntry = safeArchiveEntry

func planEditableSkillArchiveImport(files []*zip.File) ([]editableSkillArchiveImportEntry, error) {
	policy := editableSkillArchivePolicy()
	candidates, err := collectSafeArchiveCandidates(files, policy)
	if err != nil {
		return nil, err
	}

	prefix := ""
	rootSkillMD := false
	for _, candidate := range candidates {
		if candidate.path == "SKILL.md" && !candidate.dir {
			rootSkillMD = true
			break
		}
	}
	if !rootSkillMD {
		firstSegment := strings.SplitN(candidates[0].path, "/", 2)[0]
		for _, candidate := range candidates {
			parts := strings.SplitN(candidate.path, "/", 2)
			if parts[0] != firstSegment {
				return nil, skillArchiveValidationError("missing_skill_md", "ZIP must contain SKILL.md at its root or inside one top-level directory", "SKILL.md")
			}
		}
		wrappedSkillMD := firstSegment + "/SKILL.md"
		for _, candidate := range candidates {
			if candidate.path == wrappedSkillMD && !candidate.dir {
				prefix = firstSegment + "/"
				rootSkillMD = true
				break
			}
		}
	}
	if !rootSkillMD {
		return nil, skillArchiveValidationError("missing_skill_md", "ZIP must contain a root SKILL.md file", "SKILL.md")
	}
	return finalizeSafeArchiveEntries(candidates, prefix, policy)
}

func extractEditableSkillArchiveEntry(stagingDir string, entry editableSkillArchiveImportEntry, remainingArchiveBytes int64) (int64, error) {
	return extractSafeArchiveEntry(stagingDir, entry, remainingArchiveBytes, editableSkillArchivePolicy())
}

func editableSkillArchivePolicy() safeArchivePolicy {
	return safeArchivePolicy{
		subject:         "skill",
		maxFiles:        EditableSkillMaxArchiveFiles,
		maxFileBytes:    EditableSkillMaxUploadBytes,
		maxArchiveBytes: EditableSkillMaxArchiveBytes,
		tooManyFiles:    ErrSkillArchiveTooManyFiles,
		fileTooLarge:    ErrSkillFileTooLarge,
		archiveTooLarge: ErrSkillArchiveTooLarge,
		validationError: skillArchiveValidationError,
		directoryMode:   func(string) fs.FileMode { return 0o755 },
		parentMode:      func(string) fs.FileMode { return 0o755 },
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

func validateImportedEditableSkill(skillDir string) error {
	skillPath := filepath.Join(skillDir, "SKILL.md")
	info, err := os.Lstat(skillPath)
	if errors.Is(err, os.ErrNotExist) {
		return skillArchiveValidationError("missing_skill_md", "SKILL.md is required", "SKILL.md")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return skillArchiveValidationError("invalid_skill_md", "SKILL.md must be a regular file", "SKILL.md")
	}
	if info.Size() > EditableSkillMaxTextBytes {
		return skillArchiveValidationError("skill_md_too_large", "SKILL.md exceeds the maximum text size", "SKILL.md")
	}
	if err := validateEditableSkillSpecialFile(skillPath, "SKILL.md"); err != nil {
		return skillArchiveValidationError("invalid_skill_md", err.Error(), "SKILL.md")
	}
	envPath := filepath.Join(skillDir, ".runtime-env.json")
	if envInfo, err := os.Lstat(envPath); err == nil {
		if !envInfo.Mode().IsRegular() {
			return skillArchiveValidationError("invalid_runtime_env", ".runtime-env.json must be a regular file", ".runtime-env.json")
		}
		if envInfo.Size() > EditableSkillMaxTextBytes {
			return skillArchiveValidationError("runtime_env_too_large", ".runtime-env.json exceeds the maximum text size", ".runtime-env.json")
		}
		if err := validateEditableSkillSpecialFile(envPath, ".runtime-env.json"); err != nil {
			return skillArchiveValidationError("invalid_runtime_env", err.Error(), ".runtime-env.json")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	diagnostics, err := validateEditableSkillRuntimeFiles(skillDir)
	if err != nil {
		return err
	}
	if len(diagnostics) > 0 {
		items := make([]SkillArchiveDiagnostic, 0, len(diagnostics))
		for _, diagnostic := range diagnostics {
			items = append(items, SkillArchiveDiagnostic{
				Code:       diagnostic.Code,
				Message:    diagnostic.Message,
				SourcePath: archiveDiagnosticRelativePath(skillDir, diagnostic.SourcePath),
			})
		}
		return &SkillArchiveValidationError{Diagnostics: items}
	}
	return nil
}

func skillArchiveValidationError(code string, message string, sourcePath string) error {
	return &SkillArchiveValidationError{Diagnostics: []SkillArchiveDiagnostic{{
		Code:       strings.TrimSpace(code),
		Message:    strings.TrimSpace(message),
		SourcePath: filepath.ToSlash(strings.TrimSpace(sourcePath)),
	}}}
}

func archiveDiagnosticRelativePath(root string, sourcePath string) string {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return ""
	}
	relPath, err := filepath.Rel(root, sourcePath)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return filepath.ToSlash(sourcePath)
	}
	return filepath.ToSlash(relPath)
}

func cleanupEditableSkillImportStaging(root string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), editableSkillImportStagingPrefix) &&
			!strings.HasPrefix(entry.Name(), skillPackageImportStagingPrefix) &&
			!strings.HasPrefix(entry.Name(), skillPackageBackupPrefix) {
			continue
		}
		pathOnDisk := filepath.Join(root, entry.Name())
		info, err := os.Lstat(pathOnDisk)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(pathOnDisk); err != nil {
				return err
			}
			continue
		}
		if err := os.RemoveAll(pathOnDisk); err != nil {
			return err
		}
	}
	return nil
}

type editableSkillArchiveFile struct {
	path string
	size int64
}

func editableSkillArchiveFiles(skillDir string) ([]editableSkillArchiveFile, error) {
	files := []editableSkillArchiveFile{}
	var totalSize int64
	err := filepath.WalkDir(skillDir, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == skillDir {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relPath, err := filepath.Rel(skillDir, current)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if relPath == ".runtime-env.json" {
			return nil
		}
		if info.Size() > EditableSkillMaxArchiveBytes-totalSize {
			return ErrSkillArchiveTooLarge
		}
		totalSize += info.Size()
		files = append(files, editableSkillArchiveFile{path: relPath, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].path < files[j].path
	})
	return files, nil
}

func writeEditableSkillArchiveFile(archive *zip.Writer, root *os.Root, candidate editableSkillArchiveFile) error {
	cleanPath, err := validateEditableSkillRelativePath(candidate.path)
	if err != nil {
		return err
	}
	info, err := root.Lstat(cleanPath)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSkillNotFound
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSkillSymlink
	}
	if !info.Mode().IsRegular() || info.Size() != candidate.size {
		return fmt.Errorf("%w: archive source changed", ErrSkillConflict)
	}
	input, err := root.Open(cleanPath)
	if err != nil {
		return err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("%w: archive source changed", ErrSkillConflict)
	}

	header := &zip.FileHeader{Name: filepath.ToSlash(cleanPath), Method: zip.Deflate}
	header.Modified = info.ModTime().UTC()
	header.SetMode(info.Mode())
	output, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return err
}
