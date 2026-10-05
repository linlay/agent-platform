package catalog

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/skillmeta"
)

func buildAdminSkill(root string, id string, usedBy []string, includeFiles bool) (AdminSkill, error) {
	if err := ValidateEditableSkillID(id); err != nil {
		return AdminSkill{}, err
	}
	skillDir, err := editableSkillDir(root, id)
	if err != nil {
		return AdminSkill{}, err
	}
	source := EditableSkillSource{Kind: "skills-center", Path: skillDir, SkillDir: skillDir}
	item := AdminSkill{
		ID:           id,
		Name:         id,
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
			ID:              id,
			Name:            skillDisplayName(name, description, id),
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
		item.Presentation = skillmeta.Parse(metadata, version)
		for _, diagnostic := range skillMetadataDiagnostics(id, prompt) {
			item.Diagnostics = append(item.Diagnostics, skillDiagnostic(diagnostic.Severity, diagnostic.Code, diagnostic.Message, skillPath))
		}
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
	iconPath, err := resolveAdminSkillIcon(skillDir, id)
	if err != nil {
		return AdminSkill{}, err
	}
	item.IconPath = iconPath
	if hasSkillDiagnosticError(item.Diagnostics) {
		item.Status = AdminSkillStatusInvalid
	}
	return item, nil
}

func resolveAdminSkillIcon(skillDir string, id string) (string, error) {
	id = path.Base(id)
	for _, name := range []string{"icon.svg", "icon.png", strings.TrimSpace(id) + ".svg", strings.TrimSpace(id) + ".png"} {
		relPath := path.Join("assets", name)
		pathOnDisk, cleanPath, err := resolveEditableSkillPath(skillDir, relPath)
		if err != nil {
			return "", err
		}
		info, err := os.Lstat(pathOnDisk)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() {
			return filepath.ToSlash(cleanPath), nil
		}
	}
	return "", nil
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

func agentLocalSkillExists(source EditableAgentSource, id string) bool {
	if source.Kind != "directory" || strings.TrimSpace(source.AgentDir) == "" || strings.Contains(id, "/") {
		return false
	}
	root := filepath.Join(source.AgentDir, "skills")
	dir, err := editableSkillDir(root, id)
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
