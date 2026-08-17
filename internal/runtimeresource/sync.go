package runtimeresource

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	stateDirectoryName = ".agent-platform"
	stateFileName      = "runtime-resource-state.json"
	journalFileName    = "runtime-resource-upgrade.json"
	backupDirectory    = "resource-backups"
)

func Sync(options Options) (Result, error) {
	resolved, err := validateOptions(options)
	if err != nil {
		return Result{}, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := ensureRuntimeRoot(resolved); err != nil {
		return Result{}, fmt.Errorf("prepare Agent Platform runtime root: %w", err)
	}
	stateDir := filepath.Join(resolved, stateDirectoryName)
	if err := ensureRegularDirectory(stateDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("prepare runtime resource state directory: %w", err)
	}
	if err := securePrivateTree(stateDir); err != nil {
		return Result{}, err
	}
	statePath := filepath.Join(stateDir, stateFileName)
	journalPath := filepath.Join(stateDir, journalFileName)
	if err := recoverIncompleteUpgrade(resolved, stateDir, statePath, journalPath); err != nil {
		return Result{}, err
	}
	previousState, stateExists, err := readState(statePath)
	if err != nil {
		return Result{}, err
	}
	targetVersion := normalizeVersion(options.DesktopTo)
	if options.Mode == ModeVersionChange && stateExists && normalizeVersion(previousState.DesktopVersion) == targetVersion {
		return Result{
			Changed:        false,
			Mode:           options.Mode,
			DesktopVersion: targetVersion,
			PackageSHA256:  previousState.PackageSHA256,
			Stats:          previousState.Stats,
		}, nil
	}

	// Keep the candidate outside the private state tree. State/backup hardening
	// intentionally removes execute bits, while runtime shell resources must
	// preserve their deployed permissions until publish.
	workRoot, err := os.MkdirTemp(resolved, ".agent-platform-runtime-resource-staging-")
	if err != nil {
		return Result{}, fmt.Errorf("create runtime resource transaction staging: %w", err)
	}
	defer os.RemoveAll(workRoot)
	if err := os.Chmod(workRoot, 0o700); err != nil {
		return Result{}, err
	}
	current, err := extractArchive(options.Source, targetVersion, workRoot)
	if err != nil {
		return Result{}, err
	}
	oldUnits, oldRegistryFiles := managedOwnership(previousState, stateExists)
	if !stateExists && strings.TrimSpace(options.PreviousSource) != "" {
		oldUnits, oldRegistryFiles = inferPreviousOwnership(options, workRoot)
	}

	candidateRoot := filepath.Join(workRoot, "candidate")
	if err := buildCandidate(resolved, candidateRoot); err != nil {
		return Result{}, fmt.Errorf("build runtime resource candidate: %w", err)
	}
	managedUnits, managedRegistryFiles, stats, err := applyCandidate(
		candidateRoot,
		current,
		oldUnits,
		oldRegistryFiles,
	)
	if err != nil {
		return Result{}, err
	}
	regeneratedProviderKeys, err := applyProviderRegistration(
		candidateRoot,
		current.providerRegisterPath,
		options.DesktopDeviceID,
	)
	if err != nil {
		return Result{}, err
	}
	stats.RegeneratedProviderKeys = regeneratedProviderKeys
	if err := os.WriteFile(filepath.Join(candidateRoot, "VERSION"), []byte(current.versionRaw+"\n"), 0o600); err != nil {
		return Result{}, fmt.Errorf("write candidate VERSION: %w", err)
	}
	if err := validateCandidate(candidateRoot); err != nil {
		return Result{}, err
	}
	if options.BeforePublish != nil {
		if err := options.BeforePublish(); err != nil {
			return Result{}, fmt.Errorf("before runtime resource publish: %w", err)
		}
	}

	now := options.Now().UTC()
	transactionID, err := newTransactionID()
	if err != nil {
		return Result{}, err
	}
	backupDir := filepath.Join(
		stateDir,
		backupDirectory,
		backupLabel(options.DesktopFrom, options.DesktopTo, now, transactionID),
	)
	targets, pathModes, err := prepareBackups(resolved, backupDir)
	if err != nil {
		return Result{}, err
	}
	journal := upgradeJournal{
		SchemaVersion: 1,
		TransactionID: transactionID,
		RuntimeDir:    resolved,
		DesktopFrom:   normalizeVersionOrLegacy(options.DesktopFrom),
		DesktopTo:     targetVersion,
		Mode:          options.Mode,
		Source:        options.Source,
		PackageSHA256: current.sha256,
		BackupDir:     backupDir,
		Targets:       targets,
		PathModes:     pathModes,
		StartedAt:     now.Format(time.RFC3339Nano),
	}
	if err := atomicWriteJSON(journalPath, journal, 0o600); err != nil {
		return Result{}, fmt.Errorf("write runtime resource transaction journal: %w", err)
	}
	if err := securePrivateTree(stateDir); err != nil {
		return Result{}, err
	}

	if err := publishCandidate(resolved, candidateRoot, targets, options.AfterPublishStep); err != nil {
		return Result{}, rollbackAfterFailure(err, journal, journalPath)
	}
	state := State{
		SchemaVersion:        1,
		TransactionID:        transactionID,
		DesktopVersion:       targetVersion,
		PackageSHA256:        current.sha256,
		CompletedAt:          now.Format(time.RFC3339Nano),
		ManagedUnits:         sortedSet(managedUnits),
		ManagedRegistryFiles: sortedSet(managedRegistryFiles),
		Stats:                stats,
	}
	if err := atomicWriteJSON(statePath, state, 0o600); err != nil {
		return Result{}, rollbackAfterFailure(fmt.Errorf("commit runtime resource state: %w", err), journal, journalPath)
	}
	_ = os.Remove(journalPath)
	return Result{
		Changed:        true,
		Mode:           options.Mode,
		DesktopVersion: targetVersion,
		PackageSHA256:  current.sha256,
		BackupDir:      backupDir,
		Stats:          stats,
	}, nil
}

func validateOptions(options Options) (string, error) {
	if options.Mode != ModeVersionChange && options.Mode != ModeManualImport {
		return "", fmt.Errorf("runtime resource mode must be %q or %q", ModeVersionChange, ModeManualImport)
	}
	if strings.TrimSpace(options.Source) == "" {
		return "", fmt.Errorf("runtime resource source is required")
	}
	if strings.TrimSpace(options.DesktopFrom) == "" {
		return "", fmt.Errorf("Desktop version from is required")
	}
	if normalizeVersion(options.DesktopTo) == "" {
		return "", fmt.Errorf("Desktop version to is required")
	}
	if strings.TrimSpace(options.RuntimeDir) == "" {
		return "", fmt.Errorf("Agent Platform runtime directory is required")
	}
	resolved, err := filepath.Abs(options.RuntimeDir)
	if err != nil {
		return "", fmt.Errorf("resolve Agent Platform runtime directory: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func readState(statePath string) (State, bool, error) {
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read runtime resource state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false, fmt.Errorf("parse runtime resource state: %w", err)
	}
	if state.SchemaVersion != 1 || normalizeVersion(state.DesktopVersion) == "" || strings.TrimSpace(state.TransactionID) == "" {
		return State{}, false, fmt.Errorf("runtime resource state is invalid or unsupported")
	}
	if err := validateManagedPaths(state.ManagedUnits, state.ManagedRegistryFiles); err != nil {
		return State{}, false, fmt.Errorf("runtime resource state is unsafe: %w", err)
	}
	return state, true, nil
}

func validateManagedPaths(units, registries []string) error {
	for _, relative := range units {
		parts := strings.Split(relative, "/")
		if len(parts) != 2 || !contains(unitScopes, parts[0]) || !safeUnitName(parts[1]) {
			return fmt.Errorf("invalid managed unit path %q", relative)
		}
	}
	for _, relative := range registries {
		if relative == "registries" || !strings.HasPrefix(relative, "registries/") {
			return fmt.Errorf("invalid managed Registry path %q", relative)
		}
		cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
		if cleaned != relative || strings.Contains(relative, "../") {
			return fmt.Errorf("invalid managed Registry path %q", relative)
		}
	}
	return nil
}

func managedOwnership(state State, exists bool) (map[string]struct{}, map[string]struct{}) {
	units := map[string]struct{}{}
	registries := map[string]struct{}{}
	if !exists {
		return units, registries
	}
	for _, value := range state.ManagedUnits {
		units[value] = struct{}{}
	}
	for _, value := range state.ManagedRegistryFiles {
		registries[value] = struct{}{}
	}
	return units, registries
}

func inferPreviousOwnership(options Options, workRoot string) (map[string]struct{}, map[string]struct{}) {
	emptyUnits := map[string]struct{}{}
	emptyRegistries := map[string]struct{}{}
	previousWork := filepath.Join(workRoot, "previous")
	if err := os.MkdirAll(previousWork, 0o700); err != nil {
		return emptyUnits, emptyRegistries
	}
	previous, err := extractArchive(options.PreviousSource, "", previousWork)
	if err != nil {
		return emptyUnits, emptyRegistries
	}
	from := normalizeVersion(options.DesktopFrom)
	if !strings.EqualFold(strings.TrimSpace(options.DesktopFrom), "legacy") && from != previous.version {
		return emptyUnits, emptyRegistries
	}
	return previous.units, previous.registryFiles
}

func buildCandidate(runtimeRoot, candidateRoot string) error {
	if err := os.MkdirAll(candidateRoot, 0o700); err != nil {
		return err
	}
	for _, relative := range resourceScopes {
		source := filepath.Join(runtimeRoot, relative)
		target := filepath.Join(candidateRoot, relative)
		existed, err := copyResourcePathIfExists(source, target)
		if err != nil {
			return fmt.Errorf("copy current %s tree: %w", relative, err)
		}
		if !existed {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		info, err := os.Lstat(target)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("runtime resource scope must be a directory: %s", source)
		}
	}
	return nil
}

func applyCandidate(
	candidateRoot string,
	current archiveInventory,
	oldUnits map[string]struct{},
	oldRegistryFiles map[string]struct{},
) (map[string]struct{}, map[string]struct{}, Stats, error) {
	stats := Stats{}
	managedUnits := map[string]struct{}{}
	managedRegistryFiles := map[string]struct{}{}
	for relative := range oldUnits {
		if _, stillBundled := current.units[relative]; stillBundled {
			continue
		}
		target, err := safeJoin(candidateRoot, filepath.FromSlash(relative))
		if err != nil {
			return nil, nil, stats, err
		}
		if _, err := os.Lstat(target); err == nil {
			if err := os.RemoveAll(target); err != nil {
				return nil, nil, stats, fmt.Errorf("remove retired managed unit %s: %w", relative, err)
			}
			stats.RemovedManagedUnits++
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, stats, err
		}
	}
	for _, relative := range sortedSet(current.units) {
		source := filepath.Join(current.extractedRoot, filepath.FromSlash(relative))
		target := filepath.Join(candidateRoot, filepath.FromSlash(relative))
		if _, err := os.Lstat(target); err == nil {
			if err := os.RemoveAll(target); err != nil {
				return nil, nil, stats, fmt.Errorf("replace bundled unit %s: %w", relative, err)
			}
			stats.OverwrittenUnits++
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, stats, err
		} else {
			stats.AddedUnits++
		}
		if err := copyResourcePath(source, target); err != nil {
			return nil, nil, stats, fmt.Errorf("publish bundled unit %s into candidate: %w", relative, err)
		}
		managedUnits[relative] = struct{}{}
	}

	for relative := range oldRegistryFiles {
		if _, stillBundled := current.registryFiles[relative]; stillBundled {
			continue
		}
		target := filepath.Join(candidateRoot, filepath.FromSlash(relative))
		if info, err := os.Lstat(target); err == nil {
			if !info.Mode().IsRegular() {
				return nil, nil, stats, fmt.Errorf("retired managed Registry path is not a file: %s", relative)
			}
			if err := os.Remove(target); err != nil {
				return nil, nil, stats, err
			}
			removeEmptyParents(target, filepath.Join(candidateRoot, "registries"))
			stats.RemovedManagedRegistryFiles++
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, stats, err
		}
	}
	for _, relative := range sortedSet(current.registryFiles) {
		source := filepath.Join(current.extractedRoot, filepath.FromSlash(relative))
		target := filepath.Join(candidateRoot, filepath.FromSlash(relative))
		if info, err := os.Lstat(target); err == nil {
			if !info.Mode().IsRegular() {
				return nil, nil, stats, fmt.Errorf("Registry file/directory conflict: %s", relative)
			}
			if err := os.Remove(target); err != nil {
				return nil, nil, stats, err
			}
			stats.OverwrittenRegistryFiles++
		} else if errors.Is(err, os.ErrNotExist) {
			stats.AddedRegistryFiles++
		} else {
			return nil, nil, stats, err
		}
		if err := copyResourcePath(source, target); err != nil {
			return nil, nil, stats, fmt.Errorf("publish bundled Registry %s into candidate: %w", relative, err)
		}
		managedRegistryFiles[relative] = struct{}{}
	}
	return managedUnits, managedRegistryFiles, stats, nil
}

func prepareBackups(runtimeRoot, backupDir string) ([]backupTarget, map[string]uint32, error) {
	if err := ensureRegularDirectory(filepath.Dir(backupDir), 0o700); err != nil {
		return nil, nil, fmt.Errorf("prepare runtime resource backup root: %w", err)
	}
	if err := os.Mkdir(backupDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create runtime resource backup: %w", err)
	}
	targetNames := append(append([]string{}, resourceScopes...), "VERSION")
	pathModes, err := capturePathModes(runtimeRoot, targetNames)
	if err != nil {
		return nil, nil, fmt.Errorf("capture runtime resource permissions: %w", err)
	}
	targets := make([]backupTarget, 0, len(targetNames))
	for _, relative := range targetNames {
		existed, err := copyPathIfExists(filepath.Join(runtimeRoot, relative), filepath.Join(backupDir, relative))
		if err != nil {
			return nil, nil, fmt.Errorf("backup runtime resource %s: %w", relative, err)
		}
		targets = append(targets, backupTarget{RelativePath: relative, Existed: existed})
	}
	if err := securePrivateTree(backupDir); err != nil {
		return nil, nil, err
	}
	return targets, pathModes, nil
}

func capturePathModes(runtimeRoot string, targets []string) (map[string]uint32, error) {
	modes := map[string]uint32{}
	for _, target := range targets {
		root := filepath.Join(runtimeRoot, target)
		err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("runtime resource contains a symlink: %s", filePath)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(runtimeRoot, filePath)
			if err != nil {
				return err
			}
			modes[filepath.ToSlash(relative)] = uint32(info.Mode().Perm())
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return modes, nil
}

func publishCandidate(runtimeRoot, candidateRoot string, targets []backupTarget, afterStep func(string) error) error {
	for _, target := range targets {
		relative := target.RelativePath
		destination := filepath.Join(runtimeRoot, relative)
		candidate := filepath.Join(candidateRoot, relative)
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("replace runtime resource %s: %w", relative, err)
		}
		if _, err := os.Lstat(candidate); err == nil {
			if err := os.Rename(candidate, destination); err != nil {
				return fmt.Errorf("publish runtime resource %s: %w", relative, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if afterStep != nil {
			if err := afterStep(relative); err != nil {
				return fmt.Errorf("after publishing %s: %w", relative, err)
			}
		}
	}
	return nil
}

func recoverIncompleteUpgrade(runtimeRoot, stateDir, statePath, journalPath string) error {
	data, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read unfinished runtime resource transaction: %w", err)
	}
	var journal upgradeJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("parse unfinished runtime resource transaction: %w", err)
	}
	if err := validateJournal(journal, runtimeRoot, stateDir); err != nil {
		return err
	}
	state, exists, stateErr := readState(statePath)
	if stateErr == nil && exists && state.TransactionID == journal.TransactionID {
		if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clean committed runtime resource journal: %w", err)
		}
		return nil
	}
	if err := rollbackJournal(journal); err != nil {
		return fmt.Errorf("recover unfinished runtime resource transaction: %w", err)
	}
	if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove recovered runtime resource journal: %w", err)
	}
	return nil
}

func validateJournal(journal upgradeJournal, runtimeRoot, stateDir string) error {
	if journal.SchemaVersion != 1 || strings.TrimSpace(journal.TransactionID) == "" {
		return fmt.Errorf("unfinished runtime resource transaction is invalid")
	}
	journalRuntime, err := filepath.Abs(journal.RuntimeDir)
	if err != nil || filepath.Clean(journalRuntime) != filepath.Clean(runtimeRoot) {
		return fmt.Errorf("unfinished runtime resource transaction targets a different runtime")
	}
	expectedBackupRoot := filepath.Join(stateDir, backupDirectory)
	backup, err := filepath.Abs(journal.BackupDir)
	if err != nil || (backup != expectedBackupRoot && !strings.HasPrefix(backup, expectedBackupRoot+string(filepath.Separator))) {
		return fmt.Errorf("unfinished runtime resource transaction has an unsafe backup path")
	}
	for _, target := range journal.Targets {
		if !contains(append(append([]string{}, resourceScopes...), "VERSION"), target.RelativePath) {
			return fmt.Errorf("unfinished runtime resource transaction has an unsafe target %q", target.RelativePath)
		}
	}
	for relative := range journal.PathModes {
		cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
		first := strings.SplitN(cleaned, "/", 2)[0]
		if cleaned != relative || (first != "VERSION" && !contains(resourceScopes, first)) {
			return fmt.Errorf("unfinished runtime resource transaction has an unsafe permission path %q", relative)
		}
	}
	return nil
}

func rollbackAfterFailure(cause error, journal upgradeJournal, journalPath string) error {
	if rollbackErr := rollbackJournal(journal); rollbackErr != nil {
		return fmt.Errorf("%w; rollback incomplete: %v (journal retained at %s)", cause, rollbackErr, journalPath)
	}
	if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w; rollback succeeded but journal cleanup failed: %v", cause, err)
	}
	return cause
}

func rollbackJournal(journal upgradeJournal) error {
	var failures []string
	for _, target := range journal.Targets {
		destination := filepath.Join(journal.RuntimeDir, target.RelativePath)
		if err := os.RemoveAll(destination); err != nil {
			failures = append(failures, fmt.Sprintf("remove %s: %v", target.RelativePath, err))
			continue
		}
		if !target.Existed {
			continue
		}
		backup := filepath.Join(journal.BackupDir, target.RelativePath)
		if err := copyPath(backup, destination); err != nil {
			failures = append(failures, fmt.Sprintf("restore %s: %v", target.RelativePath, err))
		}
	}
	for relative, rawMode := range journal.PathModes {
		target, err := safeJoin(journal.RuntimeDir, filepath.FromSlash(relative))
		if err != nil {
			failures = append(failures, fmt.Sprintf("restore mode %s: %v", relative, err))
			continue
		}
		if err := os.Chmod(target, os.FileMode(rawMode)); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Sprintf("restore mode %s: %v", relative, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func newTransactionID() (string, error) {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create runtime resource transaction id: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func backupLabel(from, to string, now time.Time, transactionID string) string {
	return fmt.Sprintf(
		"%s-to-%s-%s-%s",
		sanitizeLabel(normalizeVersionOrLegacy(from)),
		sanitizeLabel(normalizeVersion(to)),
		now.Format("20060102T150405.000000000Z"),
		transactionID,
	)
}

func normalizeVersionOrLegacy(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "legacy") {
		return "legacy"
	}
	return normalizeVersion(value)
}

func sanitizeLabel(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '-' || character == '_' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
