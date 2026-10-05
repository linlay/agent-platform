package catalog

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"agent-platform/internal/skillmeta"
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
	ErrInvalidSkillID             = errors.New("invalid skill ID")
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
	Presentation skillmeta.Presentation
	ID           string
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
	ID        string
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
	keys, err := skillDirectoryIDs(root)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		item, err := buildAdminSkill(root, key, usage[key], false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func (r *FileRegistry) AdminSkill(id string) (AdminSkill, bool, error) {
	if r == nil {
		return AdminSkill{}, false, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return AdminSkill{}, false, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return AdminSkill{}, false, err
	}
	usage := r.skillUsageByAgent()
	dir, err := editableSkillDir(root, id)
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
	item, err := buildAdminSkill(root, strings.TrimSpace(id), usage[strings.TrimSpace(id)], true)
	if err != nil {
		return AdminSkill{}, false, err
	}
	return item, true, nil
}

func (r *FileRegistry) CreateEditableSkill(id string, skillMd string, files []EditableSkillInlineFile) (AdminSkill, error) {
	if r == nil {
		return AdminSkill{}, fmt.Errorf("skill registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return AdminSkill{}, fmt.Errorf("skills center directory is not configured")
	}
	if err := ValidateEditableSkillID(id); err != nil {
		return AdminSkill{}, err
	}
	if strings.TrimSpace(skillMd) == "" {
		return AdminSkill{}, fmt.Errorf("SKILL.md is required")
	}
	skillDir, err := editableSkillDir(root, id)
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
	return buildAdminSkill(root, strings.TrimSpace(id), usage[strings.TrimSpace(id)], true)
}

func (r *FileRegistry) DeleteEditableSkill(id string) error {
	mutation, err := r.BeginDeleteEditableSkill(id)
	if err != nil {
		return err
	}
	return mutation.Commit()
}

func (r *FileRegistry) EditableSkillUsage(id string) ([]string, error) {
	if err := ValidateEditableSkillID(id); err != nil {
		return nil, err
	}
	return append([]string(nil), r.skillUsageByAgent()[strings.TrimSpace(id)]...), nil
}
