package catalog

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	configpkg "agent-platform/internal/config"
)

const (
	EditableAgentMaxArchiveUploadBytes int64 = 32 << 20
	EditableAgentMaxArchiveBytes       int64 = 256 << 20
	EditableAgentMaxArchiveFiles             = 4096
	editableAgentImportStagingPrefix         = ".agent-import-"
	editableAgentImportBackupPrefix          = ".agent-import-backup-"
)

var (
	ErrAgentArchiveTooLarge       = errors.New("agent archive exceeds the maximum uncompressed size")
	ErrAgentArchiveUploadTooLarge = errors.New("agent archive exceeds the maximum upload size")
	ErrAgentArchiveTooManyFiles   = errors.New("agent archive contains too many entries")
	ErrAgentArchiveInvalid        = errors.New("agent archive is not a valid zip")
)

type AgentArchiveDiagnostic struct {
	Code       string
	Message    string
	SourcePath string
}

type AgentArchiveValidationError struct {
	Diagnostics []AgentArchiveDiagnostic
}

func (e *AgentArchiveValidationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "agent archive validation failed"
	}
	return e.Diagnostics[0].Message
}

type AgentArchiveConflictError struct {
	Key  string
	Name string
}

func (e *AgentArchiveConflictError) Error() string {
	return "agent already exists"
}

// EditableAgentArchiveMutation is an opaque, reversible replacement of one
// Agent source. The server commits it only after the catalog accepts the new
// source tree.
type EditableAgentArchiveMutation struct {
	Key            string
	Name           string
	finalDir       string
	backupPath     string
	previousSource EditableAgentSource
	lockHeld       bool
	completed      bool
}

// BeginImportEditableAgentArchive validates and stages a complete Agent ZIP,
// then atomically swaps it into the configured agents root. The archive key is
// always read from agent.yml/agent.yaml.
func (r *FileRegistry) BeginImportEditableAgentArchive(source io.ReaderAt, size int64, overwrite bool) (*EditableAgentArchiveMutation, error) {
	if r == nil {
		return nil, fmt.Errorf("agent registry is not configured")
	}
	root := strings.TrimSpace(r.cfg.Paths.AgentsDir)
	if root == "" {
		return nil, fmt.Errorf("agents directory is not configured")
	}
	r.agentArchiveMu.Lock()
	handedOff := false
	stagingDir := ""
	defer func() {
		if handedOff {
			return
		}
		if stagingDir != "" {
			_ = os.RemoveAll(stagingDir)
		}
		r.agentArchiveMu.Unlock()
	}()

	var key, name string
	var err error
	stagingDir, key, name, err = extractEditableAgentArchive(root, source, size)
	if err != nil {
		return nil, err
	}

	existing, found := r.AdminAgent(key)
	if found && !overwrite {
		return nil, &AgentArchiveConflictError{Key: key, Name: firstNonBlankString(existing.Name, key)}
	}

	finalDir := filepath.Join(root, key)
	previousSource := EditableAgentSource{}
	if found {
		previousSource = existing.Source
		if previousSource.Kind == "directory" && strings.TrimSpace(previousSource.AgentDir) != "" {
			finalDir = previousSource.AgentDir
		}
	}
	if !insideDir(root, finalDir) {
		return nil, agentArchiveValidationError("invalid_target", "agent target escapes the agents directory", "agent.yml")
	}

	previousPath := editableAgentSourceRoot(previousSource)
	if info, statErr := os.Lstat(finalDir); statErr == nil {
		if previousPath == "" || filepath.Clean(previousPath) != filepath.Clean(finalDir) {
			return nil, &AgentArchiveConflictError{Key: key, Name: firstNonBlankString(existing.Name, key)}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrAgentSourceSymlink
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	backupPath := ""
	if previousPath != "" {
		if !insideDir(root, previousPath) {
			return nil, agentArchiveValidationError("invalid_target", "existing agent source escapes the agents directory", "agent.yml")
		}
		if info, statErr := os.Lstat(previousPath); statErr != nil {
			return nil, statErr
		} else if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrAgentSourceSymlink
		}
		backupPath, err = reserveAgentArchiveBackup(root)
		if err != nil {
			return nil, err
		}
		if err := os.Rename(previousPath, backupPath); err != nil {
			return nil, err
		}
	}

	if installErr := os.Rename(stagingDir, finalDir); installErr != nil {
		if backupPath != "" {
			if restoreErr := os.Rename(backupPath, previousPath); restoreErr != nil {
				return nil, fmt.Errorf("install staged agent: %v; restore previous agent source: %w", installErr, restoreErr)
			}
		}
		return nil, installErr
	}
	stagingDir = ""
	mutation := &EditableAgentArchiveMutation{
		Key:            key,
		Name:           name,
		finalDir:       finalDir,
		backupPath:     backupPath,
		previousSource: previousSource,
		lockHeld:       true,
	}
	handedOff = true
	return mutation, nil
}

func (r *FileRegistry) RollbackEditableAgentArchiveMutation(mutation *EditableAgentArchiveMutation) error {
	if r == nil || mutation == nil || mutation.completed {
		return nil
	}
	if !mutation.lockHeld {
		return fmt.Errorf("agent archive mutation is not active")
	}
	defer func() {
		mutation.lockHeld = false
		r.agentArchiveMu.Unlock()
	}()
	if err := os.RemoveAll(mutation.finalDir); err != nil {
		return err
	}
	if mutation.backupPath != "" {
		previousPath := editableAgentSourceRoot(mutation.previousSource)
		if previousPath == "" {
			return fmt.Errorf("previous agent source is missing")
		}
		if err := os.Rename(mutation.backupPath, previousPath); err != nil {
			return err
		}
	}
	mutation.completed = true
	return nil
}

func (r *FileRegistry) CommitEditableAgentArchiveMutation(mutation *EditableAgentArchiveMutation) error {
	if r == nil || mutation == nil || mutation.completed {
		return nil
	}
	if !mutation.lockHeld {
		return fmt.Errorf("agent archive mutation is not active")
	}
	if mutation.backupPath != "" {
		if err := os.RemoveAll(mutation.backupPath); err != nil {
			return err
		}
	}
	mutation.completed = true
	mutation.lockHeld = false
	r.agentArchiveMu.Unlock()
	return nil
}

func editableAgentSourceRoot(source EditableAgentSource) string {
	if source.Kind == "directory" {
		return strings.TrimSpace(source.AgentDir)
	}
	if source.Kind == "file" {
		return strings.TrimSpace(source.Path)
	}
	return ""
}

func reserveAgentArchiveBackup(root string) (string, error) {
	backup, err := os.MkdirTemp(root, editableAgentImportBackupPrefix)
	if err != nil {
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	return backup, nil
}

func extractEditableAgentArchive(root string, source io.ReaderAt, size int64) (string, string, string, error) {
	if source == nil || size <= 0 {
		return "", "", "", ErrAgentArchiveInvalid
	}
	if size > EditableAgentMaxArchiveUploadBytes {
		return "", "", "", ErrAgentArchiveUploadTooLarge
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", "", "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", "", ErrAgentSourceSymlink
	}
	if !rootInfo.IsDir() {
		return "", "", "", fmt.Errorf("agents root is not a directory")
	}
	reader, err := zip.NewReader(source, size)
	if err != nil {
		return "", "", "", ErrAgentArchiveInvalid
	}
	entries, configName, err := planEditableAgentArchiveImport(reader.File)
	if err != nil {
		return "", "", "", err
	}
	stagingDir, err := os.MkdirTemp(root, editableAgentImportStagingPrefix)
	if err != nil {
		return "", "", "", err
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	if err := os.Chmod(stagingDir, 0o755); err != nil {
		return "", "", "", err
	}
	var extractedBytes int64
	for _, entry := range entries {
		written, err := extractEditableAgentArchiveEntry(stagingDir, entry, EditableAgentMaxArchiveBytes-extractedBytes)
		if err != nil {
			return "", "", "", err
		}
		extractedBytes += written
	}
	key, name, err := validateImportedEditableAgent(stagingDir, configName)
	if err != nil {
		return "", "", "", err
	}
	removeStaging = false
	return stagingDir, key, name, nil
}

type editableAgentArchiveImportEntry = safeArchiveEntry

func planEditableAgentArchiveImport(files []*zip.File) ([]editableAgentArchiveImportEntry, string, error) {
	policy := editableAgentArchivePolicy()
	candidates, err := collectSafeArchiveCandidates(files, policy)
	if err != nil {
		return nil, "", err
	}

	rootConfigName := func(prefix string) (string, error) {
		matches := make([]string, 0, 2)
		for _, candidate := range candidates {
			if candidate.dir {
				continue
			}
			relPath := candidate.path
			if prefix != "" {
				if !strings.HasPrefix(relPath, prefix) {
					continue
				}
				relPath = strings.TrimPrefix(relPath, prefix)
			}
			if relPath == "agent.yml" || relPath == "agent.yaml" {
				matches = append(matches, relPath)
			}
		}
		if len(matches) > 1 {
			return "", agentArchiveValidationError("ambiguous_agent_config", "ZIP must contain exactly one root agent.yml or agent.yaml file", matches[1])
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		return "", nil
	}

	prefix := ""
	configName, err := rootConfigName("")
	if err != nil {
		return nil, "", err
	}
	if configName == "" {
		firstSegment := strings.SplitN(candidates[0].path, "/", 2)[0]
		for _, candidate := range candidates {
			if strings.SplitN(candidate.path, "/", 2)[0] != firstSegment {
				return nil, "", agentArchiveValidationError("missing_agent_config", "ZIP must contain agent.yml or agent.yaml at its root or inside one top-level directory", "agent.yml")
			}
		}
		prefix = firstSegment + "/"
		configName, err = rootConfigName(prefix)
		if err != nil {
			return nil, "", err
		}
	}
	if configName == "" {
		return nil, "", agentArchiveValidationError("missing_agent_config", "ZIP must contain a root agent.yml or agent.yaml file", "agent.yml")
	}

	entries, err := finalizeSafeArchiveEntries(candidates, prefix, policy)
	return entries, configName, err
}

func extractEditableAgentArchiveEntry(stagingDir string, entry editableAgentArchiveImportEntry, remainingArchiveBytes int64) (int64, error) {
	return extractSafeArchiveEntry(stagingDir, entry, remainingArchiveBytes, editableAgentArchivePolicy())
}

func editableAgentArchivePolicy() safeArchivePolicy {
	privateConfig := func(relPath string) bool {
		return relPath == ".config" || strings.HasPrefix(relPath, ".config/")
	}
	directoryMode := func(relPath string) fs.FileMode {
		if privateConfig(relPath) {
			return 0o700
		}
		return 0o755
	}
	return safeArchivePolicy{
		subject:         "agent",
		maxFiles:        EditableAgentMaxArchiveFiles,
		maxFileBytes:    EditableAgentMaxArchiveUploadBytes,
		maxArchiveBytes: EditableAgentMaxArchiveBytes,
		tooManyFiles:    ErrAgentArchiveTooManyFiles,
		fileTooLarge:    ErrAgentArchiveTooLarge,
		archiveTooLarge: ErrAgentArchiveTooLarge,
		validationError: agentArchiveValidationError,
		directoryMode:   directoryMode,
		parentMode:      directoryMode,
		fileMode: func(relPath string, archiveMode fs.FileMode) fs.FileMode {
			mode := archiveMode.Perm() & 0o755
			if privateConfig(relPath) {
				return (mode & 0o100) | 0o600
			}
			return mode | 0o600
		},
	}
}

func validateImportedEditableAgent(stagingDir, configName string) (string, string, error) {
	configPath := filepath.Join(stagingDir, configName)
	info, err := os.Lstat(configPath)
	if err != nil {
		return "", "", agentArchiveValidationError("missing_agent_config", "agent config is required", configName)
	}
	if !info.Mode().IsRegular() {
		return "", "", agentArchiveValidationError("invalid_agent_config", "agent config must be a regular file", configName)
	}
	if info.Size() > EditableAgentMaxSourceBytes {
		return "", "", agentArchiveValidationError("agent_config_too_large", "agent config exceeds the maximum text size", configName)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(content) {
		return "", "", agentArchiveValidationError("invalid_agent_encoding", "agent config must be UTF-8 text", configName)
	}
	tree, err := configpkg.LoadYAMLTreeBytes(content)
	if err != nil {
		return "", "", agentArchiveValidationError("invalid_agent_definition", err.Error(), configName)
	}
	definition, ok := tree.(map[string]any)
	if !ok {
		return "", "", agentArchiveValidationError("invalid_agent_definition", "agent file must be a map", configName)
	}
	key := strings.TrimSpace(stringNode(definition["key"]))
	if err := validateImportedAgentDefinitionStructure(key, definition); err != nil {
		return "", "", agentArchiveValidationError("invalid_agent_definition", err.Error(), configName)
	}
	name := strings.TrimSpace(stringNode(definition["name"]))
	return key, firstNonBlankString(name, key), nil
}

// Import deliberately separates package identity/safety validation from
// runtime readiness. Missing local workspaces, models, tools, and skills are
// retained as an invalid AdminAgent after catalog reload so the user can fix
// the imported definition in the management UI.
func validateImportedAgentDefinitionStructure(key string, definition map[string]any) error {
	if err := validateEditableAgentKey(key); err != nil {
		return err
	}
	if _, err := ParsePublicAgentMode(stringNode(definition["mode"])); err != nil {
		return err
	}
	toolConfig := mapNode(definition["toolConfig"])
	tools := make([]string, 0)
	switch typed := toolConfig["tools"].(type) {
	case []string:
		tools = append(tools, typed...)
	case []any:
		for _, item := range typed {
			if name := strings.TrimSpace(stringNode(item)); name != "" {
				tools = append(tools, name)
			}
		}
	case string:
		if name := strings.TrimSpace(typed); name != "" {
			tools = append(tools, name)
		}
	}
	return ValidateOrdinaryAgentTools(tools)
}

func agentArchiveValidationError(code, message, sourcePath string) error {
	return &AgentArchiveValidationError{Diagnostics: []AgentArchiveDiagnostic{{Code: code, Message: message, SourcePath: sourcePath}}}
}
