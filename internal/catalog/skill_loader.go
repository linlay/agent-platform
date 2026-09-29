package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/agentconfig"
)

// ResolveSkillDefinition loads a declared skill from real host paths.
// Agent-local skills win; the skills center is used as a fallback.
func ResolveSkillDefinition(agentDir, centerDir, skillID string) (SkillDefinition, bool, error) {
	if !validSkillPathKey(skillID) {
		return SkillDefinition{}, false, ErrInvalidSkillKey
	}
	for _, skillDir := range candidateSkillDirs(agentDir, centerDir, skillID) {
		def, ok, err := loadSkillDefinitionFromDir(skillDir, skillID, 0)
		if err != nil {
			return SkillDefinition{}, false, err
		}
		if ok {
			return def, true, nil
		}
	}
	return SkillDefinition{}, false, nil
}

// ResolveRuntimeSkillDefinition loads a declared skill only from an assembled
// runtime Agent. Runtime execution must never fall back to source agents or the
// shared skills center.
func ResolveRuntimeSkillDefinition(runtimeDir, skillID string) (SkillDefinition, bool, error) {
	if !validSkillPathKey(skillID) {
		return SkillDefinition{}, false, ErrInvalidSkillKey
	}
	return loadSkillDefinitionFromDir(filepath.Join(runtimeDir, "skills", filepath.FromSlash(skillID)), skillID, 0)
}

func loadSkills(root string, maxPromptChars int) (map[string]SkillDefinition, error) {
	items := map[string]SkillDefinition{}
	keys, err := skillDirectoryKeys(root)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		definition, ok, err := loadSkillDefinitionFromDir(filepath.Join(root, filepath.FromSlash(key)), key, maxPromptChars)
		if err != nil {
			return nil, err
		}
		if !ok {
			log.Printf("[catalog][skills] skip directory %s: no SKILL.md found", key)
			continue
		}
		items[key] = definition
	}
	return items, nil
}

// skillDirectoryKeys scans the center root and follows declared package members.
// Ordinary skill subdirectories (including sub-skills) are resources, never
// implicit catalog entries.
func skillDirectoryKeys(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if !isSkillCenterDirectory(entry) {
			continue
		}
		dir := filepath.Join(root, name)
		if _, err := os.Lstat(filepath.Join(dir, "SKILL.md")); err == nil {
			keys = append(keys, name)
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, "package.json")); errors.Is(err, os.ErrNotExist) {
			keys = append(keys, name) // preserve invalid standalone diagnostics
			continue
		} else if err != nil {
			logInvalidSkillPackage(root, name, err)
			continue
		}
		manifest, err := ReadSkillPackageManifest(dir)
		if err != nil {
			logInvalidSkillPackage(root, name, err)
			continue
		}
		if manifest.Name != name {
			logInvalidSkillPackage(root, name, fmt.Errorf("%w: package name differs from directory", ErrInvalidSkillPath))
			continue
		}
		for _, member := range manifest.Skills {
			memberPath := filepath.Join(dir, member.Key, "SKILL.md")
			if err := ensureNoSymlinkAlongExistingPath(root, memberPath); err != nil {
				log.Printf("[catalog][skills] skip package member %s/%s: %v", name, member.Key, err)
				continue
			}
			if info, err := os.Lstat(memberPath); err == nil && info.Mode().IsRegular() {
				keys = append(keys, name+"/"+member.Key)
			}
		}
	}
	return keys, nil
}

func candidateSkillDirs(agentDir, centerDir, skillID string) []string {
	dirs := make([]string, 0, 2)
	if strings.TrimSpace(agentDir) != "" && !strings.Contains(skillID, "/") {
		dirs = append(dirs, filepath.Join(agentDir, "skills", skillID))
	}
	if strings.TrimSpace(centerDir) != "" {
		if dir, err := editableSkillDir(centerDir, skillID); err == nil {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func loadSkillDefinitionFromDir(skillDir, skillID string, maxPromptChars int) (SkillDefinition, bool, error) {
	if strings.TrimSpace(skillDir) == "" {
		return SkillDefinition{}, false, nil
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if errors.Is(err, os.ErrNotExist) {
		return SkillDefinition{}, false, nil
	}
	if err != nil {
		return SkillDefinition{}, false, fmt.Errorf("skill %s SKILL.md: %w", skillID, err)
	}

	prompt := strings.TrimSpace(string(content))
	name, description, triggers, metadata, version := parseSkillPromptMetadata(prompt)
	for _, diagnostic := range skillMetadataDiagnostics(skillID, prompt) {
		if diagnostic.Code == "skill_name_key_mismatch" {
			log.Printf("[catalog][skills] warning code=%s skill=%q message=%s", diagnostic.Code, skillID, diagnostic.Message)
		}
	}
	truncated := maxPromptChars > 0 && len(prompt) > maxPromptChars

	bashHooksDir, err := resolveSkillBashHooksDir(skillDir)
	if err != nil {
		return SkillDefinition{}, false, fmt.Errorf("skill %s .bash-hooks: %w", skillID, err)
	}
	runtimeEnv, err := loadSkillRuntimeEnv(skillDir)
	if err != nil {
		return SkillDefinition{}, false, fmt.Errorf("skill %s .runtime-env.json: %w", skillID, err)
	}

	return SkillDefinition{
		Key:             skillID,
		IconPath:        skillIconPath(skillDir, skillID),
		Name:            skillDisplayName(name, description, skillID),
		Description:     description,
		Triggers:        triggers,
		Metadata:        metadata,
		Version:         version,
		Prompt:          prompt,
		PromptTruncated: truncated,
		BashHooksDir:    bashHooksDir,
		RuntimeEnv:      runtimeEnv,
	}, true, nil
}

func resolveSkillBashHooksDir(skillDir string) (string, error) {
	path := filepath.Join(skillDir, ".bash-hooks")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return filepath.Abs(path)
}

func loadSkillRuntimeEnv(skillDir string) (map[string]string, error) {
	path := filepath.Join(skillDir, ".runtime-env.json")
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if err := json.Unmarshal(content, &env); err != nil {
		return nil, err
	}
	if err := agentconfig.ValidateUserEnvironment(env); err != nil {
		return nil, err
	}
	return env, nil
}

func insideDir(parent, child string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
