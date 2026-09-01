package catalog

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	AdminSkillStatusReady   = "ready"
	AdminSkillStatusInvalid = "invalid"

	EditableSkillMaxTextBytes    int64 = 1 << 20
	EditableSkillMaxUploadBytes  int64 = 32 << 20
	EditableSkillMaxArchiveBytes int64 = 256 << 20
	EditableSkillMaxArchiveFiles       = 4096

	editableSkillImportStagingPrefix = ".skill-import-"
)

var (
	ErrSkillAlreadyExists         = errors.New("skill already exists")
	ErrSkillNotFound              = errors.New("skill not found")
	ErrInvalidSkillKey            = errors.New("invalid skill key")
	ErrInvalidSkillPath           = errors.New("invalid skill path")
	ErrSkillFileTooLarge          = errors.New("skill file too large")
	ErrSkillArchiveTooLarge       = errors.New("skill archive exceeds the maximum uncompressed size")
	ErrSkillArchiveUploadTooLarge = errors.New("skill archive exceeds the maximum upload size")
	ErrSkillArchiveTooManyFiles   = errors.New("skill archive contains too many entries")
	ErrSkillArchiveInvalid        = errors.New("skill archive is not a valid zip")
	ErrSkillFileBinary            = errors.New("skill file is binary")
	ErrSkillConflict              = errors.New("skill file conflict")
	ErrSkillUnsupportedEncoding   = errors.New("unsupported skill file encoding")
	ErrSkillSymlink               = errors.New("skill path contains symlink")
	ErrSkillIsDirectory           = errors.New("skill path is a directory")
	ErrSkillDirectoryNotEmpty     = errors.New("skill directory is not empty")
)

type SkillArchiveDiagnostic struct {
	Code       string
	Message    string
	SourcePath string
}

type SkillArchiveValidationError struct {
	Diagnostics []SkillArchiveDiagnostic
}

func (e *SkillArchiveValidationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "skill archive validation failed"
	}
	return e.Diagnostics[0].Message
}

type EditableSkillSource struct {
	Kind     string
	Path     string
	SkillDir string
}

type AdminSkillDiagnostic struct {
	Severity   string
	Code       string
	Message    string
	SourcePath string
}

type AdminSkill struct {
	Key          string
	Name         string
	Description  string
	IconPath     string
	Meta         map[string]any
	Version      string
	Status       string
	Diagnostics  []AdminSkillDiagnostic
	Source       EditableSkillSource
	SkillMd      string
	Files        []EditableSkillFile
	UpdatedAt    int64
	Size         int64
	UsedByAgents []string
}

type EditableSkillFile struct {
	Path      string
	Name      string
	Kind      string
	Size      int64
	UpdatedAt int64
	MimeType  string
	Text      bool
	Binary    bool
	SHA256    string
}

type EditableSkillInlineFile struct {
	Path     string
	Content  string
	Encoding string
}

type EditableSkillFileContent struct {
	Key       string
	Path      string
	Content   string
	Encoding  string
	SHA256    string
	Size      int64
	UpdatedAt int64
}

func (r *FileRegistry) AdminSkills() ([]AdminSkill, error) {
	if r == nil {
		return nil, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return nil, fmt.Errorf("skills center directory is not configured")
	}
	usage := r.skillUsageByAgent()
	items := []AdminSkill{}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if !entry.IsDir() || strings.HasPrefix(name, ".") || !ShouldLoadRuntimeName(name) {
			continue
		}
		item, err := buildAdminSkill(root, name, usage[name], false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	return items, nil
}

func (r *FileRegistry) AdminSkill(key string) (AdminSkill, bool, error) {
	if r == nil {
		return AdminSkill{}, false, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return AdminSkill{}, false, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return AdminSkill{}, false, err
	}
	usage := r.skillUsageByAgent()
	dir, err := editableSkillDir(root, key)
	if err != nil {
		return AdminSkill{}, false, err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return AdminSkill{}, false, nil
	}
	if err != nil {
		return AdminSkill{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return AdminSkill{}, false, ErrSkillSymlink
	}
	if !info.IsDir() {
		return AdminSkill{}, false, fmt.Errorf("%w: skill root is not a directory", ErrInvalidSkillPath)
	}
	item, err := buildAdminSkill(root, strings.TrimSpace(key), usage[strings.TrimSpace(key)], true)
	if err != nil {
		return AdminSkill{}, false, err
	}
	return item, true, nil
}

func (r *FileRegistry) CreateEditableSkill(key string, skillMd string, files []EditableSkillInlineFile) (AdminSkill, error) {
	if r == nil {
		return AdminSkill{}, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return AdminSkill{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return AdminSkill{}, err
	}
	if strings.TrimSpace(skillMd) == "" {
		return AdminSkill{}, fmt.Errorf("SKILL.md is required")
	}
	skillDir, err := editableSkillDir(root, key)
	if err != nil {
		return AdminSkill{}, err
	}
	if _, err := os.Lstat(skillDir); err == nil {
		return AdminSkill{}, ErrSkillAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return AdminSkill{}, err
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return AdminSkill{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(skillDir)
		}
	}()
	if err := writeEditableSkillTextFile(skillDir, "SKILL.md", skillMd, "utf-8", ""); err != nil {
		return AdminSkill{}, err
	}
	for _, file := range files {
		cleanPath := path.Clean(strings.TrimSpace(file.Path))
		if cleanPath == "SKILL.md" {
			return AdminSkill{}, fmt.Errorf("files must not include SKILL.md")
		}
		if err := writeEditableSkillTextFile(skillDir, file.Path, file.Content, file.Encoding, ""); err != nil {
			return AdminSkill{}, err
		}
	}
	cleanup = false
	usage := r.skillUsageByAgent()
	return buildAdminSkill(root, strings.TrimSpace(key), usage[strings.TrimSpace(key)], true)
}

func (r *FileRegistry) DeleteEditableSkill(key string) error {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return err
	}
	owners, err := readSkillPackageOwners(root)
	if err != nil {
		return err
	}
	if owner := owners[strings.TrimSpace(key)]; owner != "" {
		return fmt.Errorf("%w: skill %s belongs to package %s", ErrSkillPackageConflict, strings.TrimSpace(key), owner)
	}
	dir, err := editableSkillDir(root, key)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSkillNotFound
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSkillSymlink
	}
	return os.RemoveAll(dir)
}

func (r *FileRegistry) EditableSkillUsage(key string) ([]string, error) {
	if err := ValidateEditableSkillKey(key); err != nil {
		return nil, err
	}
	return append([]string(nil), r.skillUsageByAgent()[strings.TrimSpace(key)]...), nil
}

func (r *FileRegistry) ReadEditableSkillFile(key string, relPath string) (EditableSkillFileContent, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFileContent{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return EditableSkillFileContent{}, err
	}
	skillDir, err := editableSkillDir(root, key)
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
		Key:       strings.TrimSpace(key),
		Path:      filepath.ToSlash(cleanRel),
		Content:   string(data),
		Encoding:  "utf-8",
		SHA256:    sha256Hex(data),
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UnixMilli(),
	}, nil
}

func (r *FileRegistry) ResolveEditableSkillFile(key string, relPath string) (string, EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return "", EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return "", EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, key)
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

// WriteEditableSkillArchive writes a portable ZIP archive of a skill's safe,
// distributable files. Runtime environment configuration is intentionally
// omitted because it may contain credentials.
func (r *FileRegistry) WriteEditableSkillArchive(key string, destination io.Writer) error {
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
	if err := ValidateEditableSkillKey(key); err != nil {
		return err
	}
	skillDir, err := editableSkillDir(root, key)
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
func (r *FileRegistry) ImportEditableSkillArchive(key string, source io.ReaderAt, size int64) (AdminSkill, error) {
	if r == nil {
		return AdminSkill{}, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return AdminSkill{}, fmt.Errorf("skills center directory is not configured")
	}
	key = strings.TrimSpace(key)
	finalDir, err := importEditableSkillArchiveIntoRoot(root, key, source, size)
	if err != nil {
		return AdminSkill{}, err
	}
	usage := r.skillUsageByAgent()
	item, err := buildAdminSkill(root, key, usage[key], true)
	if err != nil {
		_ = os.RemoveAll(finalDir)
		return AdminSkill{}, err
	}
	if item.Status != AdminSkillStatusReady {
		diagnostics := make([]SkillArchiveDiagnostic, 0, len(item.Diagnostics))
		for _, diagnostic := range item.Diagnostics {
			diagnostics = append(diagnostics, SkillArchiveDiagnostic{
				Code:       diagnostic.Code,
				Message:    diagnostic.Message,
				SourcePath: archiveDiagnosticRelativePath(finalDir, diagnostic.SourcePath),
			})
		}
		_ = os.RemoveAll(finalDir)
		return AdminSkill{}, &SkillArchiveValidationError{Diagnostics: diagnostics}
	}
	return item, nil
}

// importEditableSkillArchiveIntoRoot contains the shared safe ZIP extraction
// path for center and Agent-private skills. The caller owns the returned
// directory and must remove it when a larger mutation subsequently fails.
func importEditableSkillArchiveIntoRoot(root string, key string, source io.ReaderAt, size int64) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("skill root is required")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
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
	finalDir, err := editableSkillDir(root, key)
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

// DetectEditableSkillArchiveKey returns the package-defined skill key without
// extracting it. A package may explicitly set frontmatter.key; otherwise its
// required frontmatter.name is the stable key. This lets Agent-private imports
// preserve the identity supplied by the ZIP itself.
func DetectEditableSkillArchiveKey(source io.ReaderAt, size int64) (string, error) {
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
	key := strings.TrimSpace(frontMatterString(frontmatter["key"]))
	if key == "" {
		key = strings.TrimSpace(frontMatterString(frontmatter["name"]))
	}
	if key == "" {
		return "", skillArchiveValidationError("missing_skill_name", "SKILL.md frontmatter.name is required to derive the skill key", "SKILL.md")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return "", skillArchiveValidationError("invalid_skill_key", "SKILL.md frontmatter.name or key must be a valid skill key", "SKILL.md")
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
	header.SetModTime(info.ModTime())
	header.SetMode(info.Mode())
	output, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return err
}

func (r *FileRegistry) WriteEditableSkillFile(key string, relPath string, content string, encoding string, baseSHA256 string) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, key)
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

func (r *FileRegistry) DeleteEditableSkillFile(key string, relPath string, recursive bool, baseSHA256 string) error {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return err
	}
	skillDir, err := editableSkillDir(root, key)
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

func (r *FileRegistry) MkdirEditableSkillFile(key string, relPath string) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, key)
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

func (r *FileRegistry) RenameEditableSkillFile(key string, fromPath string, toPath string, overwrite bool) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, key)
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

func (r *FileRegistry) UploadEditableSkillFile(key string, relPath string, src io.Reader, overwrite bool) (EditableSkillFile, error) {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return EditableSkillFile{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return EditableSkillFile{}, err
	}
	skillDir, err := editableSkillDir(root, key)
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

func ValidateEditableSkillKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("%w: skill key is required", ErrInvalidSkillKey)
	}
	if key == "." || key == ".." || strings.HasPrefix(key, ".") {
		return ErrInvalidSkillKey
	}
	if !ShouldLoadRuntimeName(key) {
		return ErrInvalidSkillKey
	}
	if filepath.IsAbs(key) || strings.ContainsAny(key, `/\`) || strings.Contains(key, "\x00") {
		return ErrInvalidSkillKey
	}
	if filepath.Clean(key) != key {
		return ErrInvalidSkillKey
	}
	return nil
}

func buildAdminSkill(root string, key string, usedBy []string, includeFiles bool) (AdminSkill, error) {
	if err := ValidateEditableSkillKey(key); err != nil {
		return AdminSkill{}, err
	}
	skillDir, err := editableSkillDir(root, key)
	if err != nil {
		return AdminSkill{}, err
	}
	source := EditableSkillSource{Kind: "skills-center", Path: skillDir, SkillDir: skillDir}
	item := AdminSkill{
		Key:          key,
		Name:         key,
		Status:       AdminSkillStatusReady,
		Source:       source,
		UsedByAgents: append([]string(nil), usedBy...),
	}
	sort.Strings(item.UsedByAgents)

	skillPath := filepath.Join(skillDir, "SKILL.md")
	content, err := os.ReadFile(skillPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		item.Status = AdminSkillStatusInvalid
		item.Diagnostics = append(item.Diagnostics, skillDiagnostic("error", "missing_skill_md", "SKILL.md is required", skillPath))
	case err != nil:
		return AdminSkill{}, err
	default:
		item.SkillMd = string(content)
		prompt := strings.TrimSpace(string(content))
		if prompt == "" {
			item.Status = AdminSkillStatusInvalid
			item.Diagnostics = append(item.Diagnostics, skillDiagnostic("error", "empty_skill_md", "SKILL.md must not be empty", skillPath))
		}
		name, description, triggers, metadata, version := parseSkillPromptMetadata(prompt)
		def := SkillDefinition{
			Key:             key,
			Name:            skillDisplayName(name, description, key),
			Description:     description,
			Triggers:        triggers,
			Metadata:        metadata,
			Version:         version,
			Prompt:          prompt,
			PromptTruncated: false,
		}
		item.Name = def.Name
		item.Description = def.Description
		item.Version = version
		item.Meta = skillSummaryMeta(def)
	}
	if item.Meta == nil {
		item.Meta = map[string]any{"promptTruncated": false}
	}

	if diagnostics, err := validateEditableSkillRuntimeFiles(skillDir); err != nil {
		return AdminSkill{}, err
	} else if len(diagnostics) > 0 {
		item.Diagnostics = append(item.Diagnostics, diagnostics...)
	}

	files, totalSize, updatedAt, diagnostics, err := scanEditableSkillFiles(skillDir, includeFiles)
	if err != nil {
		return AdminSkill{}, err
	}
	if len(diagnostics) > 0 {
		item.Diagnostics = append(item.Diagnostics, diagnostics...)
	}
	item.Files = files
	item.Size = totalSize
	item.UpdatedAt = updatedAt
	iconPath, err := resolveAdminSkillIcon(skillDir, key)
	if err != nil {
		return AdminSkill{}, err
	}
	item.IconPath = iconPath
	if hasSkillDiagnosticError(item.Diagnostics) {
		item.Status = AdminSkillStatusInvalid
	}
	return item, nil
}

func resolveAdminSkillIcon(skillDir string, key string) (string, error) {
	relPath := path.Join("assets", strings.TrimSpace(key)+".png")
	pathOnDisk, cleanPath, err := resolveEditableSkillPath(skillDir, relPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(pathOnDisk)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil
	}
	return filepath.ToSlash(cleanPath), nil
}

func validateEditableSkillRuntimeFiles(skillDir string) ([]AdminSkillDiagnostic, error) {
	diagnostics := []AdminSkillDiagnostic{}
	hooksPath := filepath.Join(skillDir, ".bash-hooks")
	if info, err := os.Lstat(hooksPath); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			diagnostics = append(diagnostics, skillDiagnostic("error", "invalid_bash_hooks", ".bash-hooks must not be a symlink", hooksPath))
		case !info.IsDir():
			diagnostics = append(diagnostics, skillDiagnostic("error", "invalid_bash_hooks", ".bash-hooks must be a directory", hooksPath))
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	envPath := filepath.Join(skillDir, ".runtime-env.json")
	if content, err := os.ReadFile(envPath); err == nil {
		var env map[string]string
		if jsonErr := json.Unmarshal(content, &env); jsonErr != nil {
			diagnostics = append(diagnostics, skillDiagnostic("error", "invalid_runtime_env", ".runtime-env.json must be a JSON object with string values", envPath))
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return diagnostics, nil
}

func scanEditableSkillFiles(skillDir string, includeFiles bool) ([]EditableSkillFile, int64, int64, []AdminSkillDiagnostic, error) {
	files := []EditableSkillFile{}
	diagnostics := []AdminSkillDiagnostic{}
	var totalSize int64
	var updatedAt int64
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
		if info.ModTime().UnixMilli() > updatedAt {
			updatedAt = info.ModTime().UnixMilli()
		}
		if info.Mode()&os.ModeSymlink != 0 {
			diagnostics = append(diagnostics, skillDiagnostic("warning", "symlink_skipped", "symlink is not followed", current))
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		if !includeFiles {
			if entry.IsDir() {
				return nil
			}
			return nil
		}
		rel, err := filepath.Rel(skillDir, current)
		if err != nil {
			return err
		}
		file, err := editableSkillFileMetadataFromInfo(current, rel, info)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		return nil, 0, 0, nil, err
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Kind != files[j].Kind {
			return files[i].Kind < files[j].Kind
		}
		return files[i].Path < files[j].Path
	})
	return files, totalSize, updatedAt, diagnostics, nil
}

func editableSkillFileMetadata(pathOnDisk string, relPath string) (EditableSkillFile, error) {
	info, err := os.Lstat(pathOnDisk)
	if err != nil {
		return EditableSkillFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EditableSkillFile{}, ErrSkillSymlink
	}
	return editableSkillFileMetadataFromInfo(pathOnDisk, relPath, info)
}

func editableSkillFileMetadataFromInfo(pathOnDisk string, relPath string, info os.FileInfo) (EditableSkillFile, error) {
	file := EditableSkillFile{
		Path:      filepath.ToSlash(relPath),
		Name:      info.Name(),
		Kind:      "file",
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UnixMilli(),
	}
	if info.IsDir() {
		file.Kind = "directory"
		file.Size = 0
		return file, nil
	}
	file.MimeType = editableSkillMimeType(pathOnDisk)
	file.SHA256 = ""
	data, err := readSmallFilePrefix(pathOnDisk, EditableSkillMaxTextBytes+1)
	if err != nil {
		return EditableSkillFile{}, err
	}
	file.Text = int64(len(data)) <= EditableSkillMaxTextBytes && isEditableSkillText(data)
	file.Binary = !file.Text
	if sha, err := sha256File(pathOnDisk); err == nil {
		file.SHA256 = sha
	}
	return file, nil
}

func editableSkillMimeType(pathOnDisk string) string {
	if byExt := mime.TypeByExtension(filepath.Ext(pathOnDisk)); strings.TrimSpace(byExt) != "" {
		return byExt
	}
	data, err := readSmallFilePrefix(pathOnDisk, 512)
	if err != nil || len(data) == 0 {
		return ""
	}
	return http.DetectContentType(data)
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

func editableSkillDir(root string, key string) (string, error) {
	if err := ValidateEditableSkillKey(key); err != nil {
		return "", err
	}
	dir := filepath.Join(root, strings.TrimSpace(key))
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

func (r *FileRegistry) skillUsageByAgent() map[string][]string {
	usage := map[string][]string{}
	if r == nil {
		return usage
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	add := func(agentKey string, source EditableAgentSource, skills []string) {
		agentKey = strings.TrimSpace(agentKey)
		if agentKey == "" {
			return
		}
		for _, raw := range skills {
			key := strings.TrimSpace(raw)
			if key == "" {
				continue
			}
			if agentLocalSkillExists(source, key) {
				continue
			}
			if !containsString(usage[key], agentKey) {
				usage[key] = append(usage[key], agentKey)
			}
		}
	}
	if len(r.adminAgents) > 0 {
		for _, key := range sortedKeys(r.adminAgents) {
			item := r.adminAgents[key]
			add(item.Key, item.Source, item.Skills)
		}
	} else {
		for _, key := range sortedKeys(r.agents) {
			item := r.agents[key]
			add(item.Key, EditableAgentSource{Kind: "directory", AgentDir: item.AgentDir}, item.Skills)
		}
	}
	for key := range usage {
		sort.Strings(usage[key])
	}
	return usage
}

func agentLocalSkillExists(source EditableAgentSource, key string) bool {
	if source.Kind != "directory" || strings.TrimSpace(source.AgentDir) == "" {
		return false
	}
	root := filepath.Join(source.AgentDir, "skills")
	dir, err := editableSkillDir(root, key)
	if err != nil {
		return false
	}
	info, err := os.Lstat(dir)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir()
}

func skillDiagnostic(severity string, code string, message string, sourcePath string) AdminSkillDiagnostic {
	return AdminSkillDiagnostic{
		Severity:   severity,
		Code:       code,
		Message:    message,
		SourcePath: sourcePath,
	}
}

func hasSkillDiagnosticError(items []AdminSkillDiagnostic) bool {
	for _, item := range items {
		if item.Severity == "error" {
			return true
		}
	}
	return false
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
