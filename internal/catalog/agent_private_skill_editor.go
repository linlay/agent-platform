package catalog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/contracts"
)

var (
	ErrAgentPrivateSkillDirectoryRequired = errors.New("agent-private skills require a directory agent")
)

// EditableAgentPrivateSkillMutation is an opaque, reversible filesystem and
// Agent-definition mutation. Server handlers commit it only after the catalog
// reload accepts the new source state.
type EditableAgentPrivateSkillMutation struct {
	action    string
	agentKey  string
	skillKey  string
	previous  EditableAgentFiles
	finalDir  string
	stagedDir string
	lockHeld  bool
	completed bool
}

// EditableAgentPrivateSkills lists every local skill directory for one Agent,
// including locally installed skills that are currently disabled in agent.yml.
func (r *FileRegistry) EditableAgentPrivateSkills(agentKey string) ([]AdminAgentPrivateSkill, error) {
	if r == nil {
		return nil, fmt.Errorf("agent registry is not configured")
	}
	item, found := r.AdminAgent(strings.TrimSpace(agentKey))
	if !found {
		return nil, ErrAgentSourceNotFound
	}
	files := EditableAgentFiles{
		Key:          item.Key,
		Definition:   contracts.CloneMap(item.Definition),
		SoulPrompt:   item.SoulPrompt,
		AgentsPrompt: item.AgentsPrompt,
		Source:       item.Source,
	}
	if files.Source.Kind != "directory" || strings.TrimSpace(files.Source.AgentDir) == "" {
		return []AdminAgentPrivateSkill{}, nil
	}
	return r.listEditableAgentPrivateSkills(files)
}

func (r *FileRegistry) listEditableAgentPrivateSkills(files EditableAgentFiles) ([]AdminAgentPrivateSkill, error) {
	root, err := editableAgentPrivateSkillsRoot(files.Source)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return []AdminAgentPrivateSkill{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSkillSymlink
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent private skills path is not a directory")
	}
	enabled := editableDefinitionSkillSet(files.Definition)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	items := make([]AdminAgentPrivateSkill, 0, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Name())
		if !entry.IsDir() || strings.HasPrefix(key, ".") || !ShouldLoadRuntimeName(key) {
			continue
		}
		item, err := buildAdminSkill(root, key, nil, false)
		if err != nil {
			return nil, err
		}
		diagnostics := make([]AdminSkillDiagnostic, 0, len(item.Diagnostics))
		for _, diagnostic := range item.Diagnostics {
			diagnostic.SourcePath = archiveDiagnosticRelativePath(item.Source.SkillDir, diagnostic.SourcePath)
			diagnostics = append(diagnostics, diagnostic)
		}
		items = append(items, AdminAgentPrivateSkill{
			Key:             item.Key,
			Name:            item.Name,
			Description:     item.Description,
			Status:          item.Status,
			Diagnostics:     diagnostics,
			Enabled:         enabled[strings.ToLower(item.Key)],
			OverridesCenter: r.centerSkillExists(item.Key),
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

// BeginImportEditableAgentPrivateSkillArchive installs a validated archive and
// adds its key to the Agent definition. The caller must rollback it when the
// following catalog reload fails.
func (r *FileRegistry) BeginImportEditableAgentPrivateSkillArchive(agentKey, key string, source io.ReaderAt, size int64) (*EditableAgentPrivateSkillMutation, error) {
	if r == nil {
		return nil, fmt.Errorf("agent registry is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	r.privateSkillMu.Lock()
	handedOff := false
	finalDir := ""
	defer func() {
		if handedOff {
			return
		}
		if finalDir != "" {
			_ = os.RemoveAll(finalDir)
		}
		r.privateSkillMu.Unlock()
	}()

	files, found, err := r.findEditableAgent(strings.TrimSpace(agentKey))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrAgentSourceNotFound
	}
	root, err := editableAgentPrivateSkillsRoot(files.Source)
	if err != nil {
		return nil, err
	}
	finalDir, err = importEditableSkillArchiveIntoRoot(root, key, source, size)
	if err != nil {
		return nil, err
	}
	definition := editableDefinitionWithSkill(files.Definition, key, true)
	if _, err := r.UpdateEditableAgent(files.Key, definition, &files.SoulPrompt, &files.AgentsPrompt); err != nil {
		return nil, err
	}
	mutation := &EditableAgentPrivateSkillMutation{
		action:   "import",
		agentKey: files.Key,
		skillKey: key,
		previous: files,
		finalDir: finalDir,
		lockHeld: true,
	}
	handedOff = true
	return mutation, nil
}

// BeginDeleteEditableAgentPrivateSkill stages a local skill for deletion and
// removes its declaration. The original directory remains recoverable until
// CommitEditableAgentPrivateSkillMutation is called.
func (r *FileRegistry) BeginDeleteEditableAgentPrivateSkill(agentKey, key string) (*EditableAgentPrivateSkillMutation, error) {
	if r == nil {
		return nil, fmt.Errorf("agent registry is not configured")
	}
	if err := ValidateEditableSkillKey(key); err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	r.privateSkillMu.Lock()
	handedOff := false
	finalDir := ""
	stagedDir := ""
	defer func() {
		if handedOff {
			return
		}
		if stagedDir != "" && finalDir != "" {
			_ = os.Rename(stagedDir, finalDir)
		}
		r.privateSkillMu.Unlock()
	}()

	files, found, err := r.findEditableAgent(strings.TrimSpace(agentKey))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrAgentSourceNotFound
	}
	root, err := editableAgentPrivateSkillsRoot(files.Source)
	if err != nil {
		return nil, err
	}
	finalDir, err = editableSkillDir(root, key)
	if err != nil {
		return nil, err
	}
	if err := ensureExistingEditableSkillDir(finalDir); err != nil {
		return nil, err
	}
	stagedDir, err = os.MkdirTemp(root, ".skill-delete-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(stagedDir); err != nil {
		return nil, err
	}
	if err := os.Rename(finalDir, stagedDir); err != nil {
		return nil, err
	}
	definition := editableDefinitionWithSkill(files.Definition, key, false)
	if _, err := r.UpdateEditableAgent(files.Key, definition, &files.SoulPrompt, &files.AgentsPrompt); err != nil {
		return nil, err
	}
	mutation := &EditableAgentPrivateSkillMutation{
		action:    "delete",
		agentKey:  files.Key,
		skillKey:  key,
		previous:  files,
		finalDir:  finalDir,
		stagedDir: stagedDir,
		lockHeld:  true,
	}
	handedOff = true
	return mutation, nil
}

func (r *FileRegistry) RollbackEditableAgentPrivateSkillMutation(mutation *EditableAgentPrivateSkillMutation) error {
	if r == nil || mutation == nil || mutation.completed {
		return nil
	}
	if !mutation.lockHeld {
		return fmt.Errorf("agent-private skill mutation is not active")
	}
	defer func() {
		mutation.lockHeld = false
		r.privateSkillMu.Unlock()
	}()
	var result error
	switch mutation.action {
	case "import":
		if err := os.RemoveAll(mutation.finalDir); err != nil {
			result = err
		}
	case "delete":
		if err := os.Rename(mutation.stagedDir, mutation.finalDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = err
		}
	}
	if _, err := r.UpdateEditableAgent(mutation.previous.Key, mutation.previous.Definition, &mutation.previous.SoulPrompt, &mutation.previous.AgentsPrompt); err != nil && result == nil {
		result = err
	}
	if result == nil {
		mutation.completed = true
	}
	return result
}

func (r *FileRegistry) CommitEditableAgentPrivateSkillMutation(mutation *EditableAgentPrivateSkillMutation) error {
	if r == nil || mutation == nil || mutation.completed {
		return nil
	}
	if !mutation.lockHeld {
		return fmt.Errorf("agent-private skill mutation is not active")
	}
	if mutation.action == "delete" {
		if err := os.RemoveAll(mutation.stagedDir); err != nil {
			return err
		}
	}
	mutation.completed = true
	mutation.lockHeld = false
	r.privateSkillMu.Unlock()
	return nil
}

func editableAgentPrivateSkillsRoot(source EditableAgentSource) (string, error) {
	if source.Kind != "directory" || strings.TrimSpace(source.AgentDir) == "" {
		return "", ErrAgentPrivateSkillDirectoryRequired
	}
	root := filepath.Join(source.AgentDir, "skills")
	if !insideDir(source.AgentDir, root) {
		return "", ErrInvalidSkillPath
	}
	if err := ensureNoSymlinkAlongExistingPath(source.AgentDir, root); err != nil {
		return "", err
	}
	return root, nil
}

func editableDefinitionSkillSet(definition map[string]any) map[string]bool {
	result := map[string]bool{}
	for _, skill := range editableDefinitionSkills(definition) {
		result[strings.ToLower(skill)] = true
	}
	return result
}

func editableDefinitionSkills(definition map[string]any) []string {
	config := contracts.AnyMapNode(definition["skillConfig"])
	value := config["skills"]
	items := []string{}
	switch typed := value.(type) {
	case []string:
		items = append(items, typed...)
	case []any:
		for _, item := range typed {
			if value := strings.TrimSpace(stringNode(item)); value != "" {
				items = append(items, value)
			}
		}
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			items = append(items, value)
		}
	}
	return items
}

func editableDefinitionWithSkill(definition map[string]any, key string, include bool) map[string]any {
	out := contracts.CloneMap(definition)
	if out == nil {
		out = map[string]any{}
	}
	config := contracts.CloneMap(contracts.AnyMapNode(out["skillConfig"]))
	if config == nil {
		config = map[string]any{}
	}
	current := editableDefinitionSkills(out)
	next := make([]string, 0, len(current)+1)
	found := false
	for _, value := range current {
		if strings.EqualFold(value, key) {
			found = true
			if !include {
				continue
			}
			next = append(next, key)
			continue
		}
		next = append(next, value)
	}
	if include && !found {
		next = append(next, key)
	}
	if len(next) == 0 {
		delete(config, "skills")
		if len(config) == 0 {
			delete(out, "skillConfig")
		} else {
			out["skillConfig"] = config
		}
		return out
	}
	config["skills"] = next
	out["skillConfig"] = config
	return out
}

func (r *FileRegistry) centerSkillExists(key string) bool {
	root := strings.TrimSpace(r.cfg.Paths.SkillsCenterDir)
	if root == "" {
		return false
	}
	dir, err := editableSkillDir(root, key)
	if err != nil {
		return false
	}
	info, err := os.Lstat(dir)
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir()
}
