package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type runtimeAgentAssemblyError struct {
	code string
	err  error
}

func (e *runtimeAgentAssemblyError) Error() string {
	if e == nil || e.err == nil {
		return "runtime agent assembly failed"
	}
	return e.err.Error()
}

func (e *runtimeAgentAssemblyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func runtimeAgentAssemblyDiagnosticCode(err error) string {
	var assemblyErr *runtimeAgentAssemblyError
	if errors.As(err, &assemblyErr) && strings.TrimSpace(assemblyErr.code) != "" {
		return assemblyErr.code
	}
	return "runtime_agent_assembly_failed"
}

type runtimeAgentAssembler struct {
	root      string
	marketDir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newRuntimeAgentAssembler(root, marketDir string) (*runtimeAgentAssembler, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("ru-agents directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve ru-agents directory: %w", err)
	}
	assembler := &runtimeAgentAssembler{
		root:      filepath.Clean(absolute),
		marketDir: strings.TrimSpace(marketDir),
		locks:     map[string]*sync.Mutex{},
	}
	if info, err := os.Lstat(assembler.root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("ru-agents directory must not be a symlink: %s", assembler.root)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect ru-agents directory: %w", err)
	}
	if err := os.MkdirAll(assembler.root, 0o700); err != nil {
		return nil, fmt.Errorf("create ru-agents directory: %w", err)
	}
	if err := os.Chmod(assembler.root, 0o700); err != nil {
		return nil, fmt.Errorf("protect ru-agents directory: %w", err)
	}
	staging := filepath.Join(assembler.root, ".staging")
	if err := os.RemoveAll(staging); err != nil {
		return nil, fmt.Errorf("clean ru-agents staging directory: %w", err)
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create ru-agents staging directory: %w", err)
	}
	return assembler, nil
}

func (a *runtimeAgentAssembler) assemble(source EditableAgentSource, def AgentDefinition) (string, error) {
	if a == nil {
		return "", fmt.Errorf("runtime agent assembler is not configured")
	}
	key := strings.TrimSpace(def.Key)
	if !validRuntimeComponent(key) {
		return "", &runtimeAgentAssemblyError{
			code: "invalid_runtime_agent_key",
			err:  fmt.Errorf("agent key %q is not a safe runtime directory name", def.Key),
		}
	}
	lock := a.agentLock(key)
	lock.Lock()
	defer lock.Unlock()

	stagingRoot := filepath.Join(a.root, ".staging")
	candidate, err := os.MkdirTemp(stagingRoot, key+"-")
	if err != nil {
		return "", fmt.Errorf("create runtime agent candidate: %w", err)
	}
	defer os.RemoveAll(candidate)
	if err := os.Chmod(candidate, 0o700); err != nil {
		return "", fmt.Errorf("protect runtime agent candidate: %w", err)
	}

	if err := copyRuntimeAgentSource(source, candidate); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(candidate, "skills"), 0o700); err != nil {
		return "", fmt.Errorf("create runtime skills directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(candidate, ".config"), 0o700); err != nil {
		return "", fmt.Errorf("create runtime config directory: %w", err)
	}
	if err := a.materializeSkills(source, candidate, def.Skills); err != nil {
		return "", err
	}
	if source.Kind == "directory" {
		if err := overlayAgentConfig(filepath.Join(source.AgentDir, ".config"), filepath.Join(candidate, ".config")); err != nil {
			return "", fmt.Errorf("apply agent .config: %w", err)
		}
	}
	if err := validateRuntimeAgentCandidate(candidate, def); err != nil {
		return "", err
	}

	stable := filepath.Join(a.root, key)
	if !insideDir(a.root, stable) {
		return "", fmt.Errorf("runtime agent target escapes ru-agents: %s", stable)
	}
	if err := syncRuntimeTree(candidate, stable); err != nil {
		return "", fmt.Errorf("publish runtime agent %s: %w", key, err)
	}
	return stable, nil
}

func (a *runtimeAgentAssembler) agentLock(key string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if lock := a.locks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	a.locks[key] = lock
	return lock
}

func (a *runtimeAgentAssembler) cleanupDeleted(expected map[string]struct{}) error {
	if a == nil {
		return nil
	}
	entries, err := os.ReadDir(a.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !entry.IsDir() {
			continue
		}
		if _, ok := expected[name]; ok {
			continue
		}
		lock := a.agentLock(name)
		lock.Lock()
		target := filepath.Join(a.root, name)
		if insideDir(a.root, target) {
			err = os.RemoveAll(target)
		}
		lock.Unlock()
		if err != nil {
			return fmt.Errorf("remove deleted runtime agent %s: %w", name, err)
		}
	}
	return nil
}

func copyRuntimeAgentSource(source EditableAgentSource, candidate string) error {
	switch source.Kind {
	case "directory":
		entries, err := os.ReadDir(source.AgentDir)
		if err != nil {
			return fmt.Errorf("read agent directory: %w", err)
		}
		for _, entry := range entries {
			if entry.Name() == "skills" || entry.Name() == ".config" {
				continue
			}
			if err := copyRuntimePath(
				filepath.Join(source.AgentDir, entry.Name()),
				filepath.Join(candidate, entry.Name()),
			); err != nil {
				return fmt.Errorf("copy agent runtime resource %s: %w", entry.Name(), err)
			}
		}
	case "file":
		if err := copyRuntimePath(source.Path, filepath.Join(candidate, "agent.yml")); err != nil {
			return fmt.Errorf("copy standalone agent definition: %w", err)
		}
	default:
		return fmt.Errorf("unsupported agent source kind %q", source.Kind)
	}
	return nil
}

func (a *runtimeAgentAssembler) materializeSkills(source EditableAgentSource, candidate string, declared []string) error {
	ordered, err := orderedSkillIDs(declared)
	if err != nil {
		return err
	}
	configEntries := map[string]runtimeConfigEntry{}
	for _, skillID := range ordered {
		skillSource, err := a.resolveSkillSource(source, skillID)
		if err != nil {
			return err
		}
		target := filepath.Join(candidate, "skills", skillID)
		if err := copyRuntimePath(skillSource, target); err != nil {
			return fmt.Errorf("copy skill %q: %w", skillID, err)
		}
		if err := collectSkillConfig(skillID, filepath.Join(skillSource, ".config"), configEntries); err != nil {
			return err
		}
	}
	return writeSkillConfigEntries(filepath.Join(candidate, ".config"), configEntries)
}

func (a *runtimeAgentAssembler) resolveSkillSource(source EditableAgentSource, skillID string) (string, error) {
	if source.Kind == "directory" {
		local := filepath.Join(source.AgentDir, "skills", skillID)
		info, err := os.Lstat(local)
		switch {
		case err == nil:
			if !info.IsDir() {
				return "", fmt.Errorf("agent-local skill %q exists but is not a directory", skillID)
			}
			if err := validateSourceSkill(local, skillID, "agent-local"); err != nil {
				return "", err
			}
			return local, nil
		case !errors.Is(err, os.ErrNotExist):
			return "", fmt.Errorf("inspect agent-local skill %q: %w", skillID, err)
		}
	}
	if strings.TrimSpace(a.marketDir) == "" {
		return "", fmt.Errorf("skill %q is not available: skills-market is not configured", skillID)
	}
	market := filepath.Join(a.marketDir, skillID)
	if !insideDir(a.marketDir, market) {
		return "", fmt.Errorf("skill %q resolves outside skills-market", skillID)
	}
	info, err := os.Lstat(market)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("skill %q does not exist in agent or skills-market", skillID)
	}
	if err != nil {
		return "", fmt.Errorf("inspect market skill %q: %w", skillID, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("market skill %q is not a directory", skillID)
	}
	if err := validateSourceSkill(market, skillID, "market"); err != nil {
		return "", err
	}
	return market, nil
}

func validateSourceSkill(dir, skillID, source string) error {
	_, ok, err := loadSkillDefinitionFromDir(dir, skillID, 0)
	if err != nil {
		return fmt.Errorf("%s skill %q is invalid: %w", source, skillID, err)
	}
	if !ok {
		return fmt.Errorf("%s skill %q is invalid: SKILL.md is required", source, skillID)
	}
	return nil
}

func orderedSkillIDs(declared []string) ([]string, error) {
	out := make([]string, 0, len(declared))
	seen := map[string]struct{}{}
	for _, raw := range declared {
		id := strings.TrimSpace(raw)
		if !validRuntimeComponent(id) {
			return nil, fmt.Errorf("skill id %q is not a safe runtime directory name", raw)
		}
		folded := strings.ToLower(id)
		if _, duplicate := seen[folded]; duplicate {
			continue
		}
		seen[folded] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func validRuntimeComponent(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" ||
		value == "." ||
		value == ".." ||
		strings.ContainsAny(value, `/\<>:"|?*`) ||
		strings.HasSuffix(value, ".") ||
		strings.HasSuffix(value, " ") {
		return false
	}
	for _, char := range value {
		if char < 0x20 {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return false
	}
	return !(len(base) == 4 &&
		(strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9')
}

type runtimeConfigEntry struct {
	rel    string
	isDir  bool
	data   []byte
	mode   os.FileMode
	source string
}

func collectSkillConfig(skillID, root string, entries map[string]runtimeConfigEntry) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skill %q .config: %w", skillID, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("skill %q .config is not a directory", skillID)
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill %q .config contains unsupported symlink %s", skillID, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		entry := runtimeConfigEntry{
			rel:    rel,
			isDir:  info.IsDir(),
			mode:   info.Mode().Perm(),
			source: skillID,
		}
		if !entry.isDir {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("skill %q .config contains unsupported file %s", skillID, path)
			}
			entry.data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		return addSkillConfigEntry(entries, entry)
	})
}

func addSkillConfigEntry(entries map[string]runtimeConfigEntry, incoming runtimeConfigEntry) error {
	key := strings.ToLower(incoming.rel)
	if existing, ok := entries[key]; ok {
		if existing.rel != incoming.rel {
			return fmt.Errorf(
				"skill .config case-fold collision at %q between %q and %q",
				incoming.rel,
				existing.source,
				incoming.source,
			)
		}
		if existing.isDir != incoming.isDir {
			return fmt.Errorf(
				"skill .config file/directory conflict at %q between %q and %q",
				incoming.rel,
				existing.source,
				incoming.source,
			)
		}
		if incoming.isDir || bytes.Equal(existing.data, incoming.data) {
			return nil
		}
		return fmt.Errorf(
			"skill .config content conflict at %q between %q and %q",
			incoming.rel,
			existing.source,
			incoming.source,
		)
	}
	for parent := filepath.ToSlash(filepath.Dir(incoming.rel)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
		if existing, ok := entries[strings.ToLower(parent)]; ok && !existing.isDir {
			return fmt.Errorf(
				"skill .config file/directory conflict at %q between %q and %q",
				parent,
				existing.source,
				incoming.source,
			)
		}
	}
	if !incoming.isDir {
		prefix := key + "/"
		for existingKey, existing := range entries {
			if strings.HasPrefix(existingKey, prefix) {
				return fmt.Errorf(
					"skill .config file/directory conflict at %q between %q and %q",
					incoming.rel,
					existing.source,
					incoming.source,
				)
			}
		}
	}
	entries[key] = incoming
	return nil
}

func writeSkillConfigEntries(root string, entries map[string]runtimeConfigEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ordered := make([]runtimeConfigEntry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftDepth := strings.Count(ordered[i].rel, "/")
		rightDepth := strings.Count(ordered[j].rel, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		if ordered[i].isDir != ordered[j].isDir {
			return ordered[i].isDir
		}
		return ordered[i].rel < ordered[j].rel
	})
	for _, entry := range ordered {
		target := filepath.Join(root, filepath.FromSlash(entry.rel))
		if !insideDir(root, target) {
			return fmt.Errorf("skill .config target escapes runtime config: %s", entry.rel)
		}
		if entry.isDir {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, entry.data, privateFileMode(entry.mode)); err != nil {
			return err
		}
	}
	return nil
}

func overlayAgentConfig(source, target string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("agent .config is not a directory")
	}
	seen := map[string]string{}
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent .config contains unsupported symlink %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		folded := strings.ToLower(rel)
		if previous, exists := seen[folded]; exists && previous != rel {
			return fmt.Errorf("agent .config contains case-fold collision between %q and %q", previous, rel)
		}
		seen[folded] = rel
		if existing, ok, err := findFoldedRelativePath(target, rel); err != nil {
			return err
		} else if ok && existing != rel {
			if err := os.RemoveAll(filepath.Join(target, filepath.FromSlash(existing))); err != nil {
				return err
			}
		}
		destination := filepath.Join(target, filepath.FromSlash(rel))
		if !insideDir(target, destination) {
			return fmt.Errorf("agent .config target escapes runtime config: %s", rel)
		}
		if info.IsDir() {
			if current, err := os.Lstat(destination); err == nil && !current.IsDir() {
				if err := os.Remove(destination); err != nil {
					return err
				}
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("agent .config contains unsupported file %s", path)
		}
		if err := os.RemoveAll(destination); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		return copyRuntimeFile(path, destination, info.Mode().Perm())
	})
}

func findFoldedRelativePath(root, rel string) (string, bool, error) {
	entries := make([]string, 0)
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("%s is not a directory", root)
	}
	err = filepath.Walk(root, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		itemRel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(itemRel))
		return nil
	})
	if err != nil {
		return "", false, err
	}
	folded := strings.ToLower(filepath.ToSlash(rel))
	sort.Slice(entries, func(i, j int) bool {
		return strings.Count(entries[i], "/") < strings.Count(entries[j], "/")
	})
	for _, item := range entries {
		if strings.ToLower(item) == folded {
			return item, true, nil
		}
	}
	return "", false, nil
}

func validateRuntimeAgentCandidate(candidate string, expected AgentDefinition) error {
	configPath := resolveDirectoryAgentConfig(candidate)
	if configPath == "" {
		return fmt.Errorf("runtime agent candidate has no agent.yml or agent.yaml")
	}
	def, _, err := parseAgentFileRaw(configPath)
	if err != nil {
		return fmt.Errorf("reload runtime agent candidate: %w", err)
	}
	if def.Key != expected.Key {
		return fmt.Errorf("runtime agent key changed during assembly: got %q want %q", def.Key, expected.Key)
	}
	ordered, err := orderedSkillIDs(expected.Skills)
	if err != nil {
		return err
	}
	for _, skillID := range ordered {
		dir := filepath.Join(candidate, "skills", skillID)
		if err := validateSourceSkill(dir, skillID, "runtime"); err != nil {
			return err
		}
	}
	return nil
}

func copyRuntimePath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinks are not supported: %s", source)
	}
	if info.IsDir() {
		return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlinks are not supported: %s", path)
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			destination := filepath.Join(target, rel)
			if !insideDir(target, destination) {
				return fmt.Errorf("copy target escapes runtime directory: %s", destination)
			}
			if info.IsDir() {
				if err := os.MkdirAll(destination, 0o700); err != nil {
					return err
				}
				return os.Chmod(destination, 0o700)
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return err
			}
			return copyRuntimeFile(path, destination, info.Mode().Perm())
		})
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported runtime resource: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return copyRuntimeFile(source, target, info.Mode().Perm())
}

func copyRuntimeFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, privateFileMode(mode))
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Chmod(target, privateFileMode(mode))
}

func privateFileMode(source os.FileMode) os.FileMode {
	return 0o600 | (source.Perm() & 0o100)
}

func syncRuntimeTree(candidate, stable string) error {
	if info, err := os.Lstat(stable); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		if err := os.Remove(stable); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(stable, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(stable, 0o700); err != nil {
		return err
	}
	expected := map[string]bool{"": true}
	if err := filepath.Walk(candidate, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(candidate, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.Clean(rel)
		target := filepath.Join(stable, rel)
		if !insideDir(stable, target) {
			return fmt.Errorf("runtime sync target escapes stable directory: %s", target)
		}
		expected[rel] = info.IsDir()
		if info.IsDir() {
			if current, err := os.Lstat(target); err == nil && !current.IsDir() {
				if err := os.Remove(target); err != nil {
					return err
				}
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, 0o700)
		}
		if current, err := os.Lstat(target); err == nil && current.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicCopyRuntimeFile(path, target, info.Mode().Perm())
	}); err != nil {
		return err
	}

	type staleEntry struct {
		path  string
		depth int
	}
	var stale []staleEntry
	if err := filepath.Walk(stable, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(stable, path)
		if err != nil || rel == "." {
			return err
		}
		if _, ok := expected[filepath.Clean(rel)]; !ok {
			stale = append(stale, staleEntry{path: path, depth: strings.Count(filepath.ToSlash(rel), "/")})
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].depth > stale[j].depth })
	for _, entry := range stale {
		if err := os.RemoveAll(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func atomicCopyRuntimeFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temp, err := os.CreateTemp(filepath.Dir(target), ".ru-sync-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(privateFileMode(mode)); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.Copy(temp, input); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		// Windows does not replace an existing destination with os.Rename.
		// The stable root remains unchanged; only this file has a short
		// replacement window, which is part of the non-snapshot hot-reload
		// contract.
		if removeErr := os.Remove(target); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		if retryErr := os.Rename(tempPath, target); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(target, privateFileMode(mode))
}
