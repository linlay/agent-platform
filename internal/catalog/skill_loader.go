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
// Agent-local skills win; the skills market is used as a fallback.
func ResolveSkillDefinition(agentDir, marketDir, skillID string) (SkillDefinition, bool, error) {
	for _, skillDir := range candidateSkillDirs(agentDir, marketDir, skillID) {
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
// shared skills market.
func ResolveRuntimeSkillDefinition(runtimeDir, skillID string) (SkillDefinition, bool, error) {
	return loadSkillDefinitionFromDir(filepath.Join(runtimeDir, "skills", skillID), skillID, 0)
}

func loadSkills(root string, maxPromptChars int) (map[string]SkillDefinition, error) {
	items := map[string]SkillDefinition{}
	var loadErr error
	err := visitRuntimeEntries(
		root,
		nil,
		func(name string, entry os.DirEntry) bool {
			return entry.IsDir() && !strings.HasPrefix(name, ".") && ShouldLoadRuntimeName(name)
		},
		func(name string, _ os.DirEntry) {
			if loadErr != nil {
				return
			}
			skillDir := filepath.Join(root, name)
			definition, ok, err := loadSkillDefinitionFromDir(skillDir, name, maxPromptChars)
			if err != nil {
				loadErr = err
				return
			}
			if !ok {
				log.Printf("[catalog][skills] skip directory %s: no SKILL.md found", name)
				return
			}
			items[name] = definition
		},
	)
	if err != nil {
		return nil, err
	}
	if loadErr != nil {
		return nil, loadErr
	}
	return items, nil
}

func candidateSkillDirs(agentDir, marketDir, skillID string) []string {
	dirs := make([]string, 0, 2)
	if strings.TrimSpace(agentDir) != "" {
		dirs = append(dirs, filepath.Join(agentDir, "skills", skillID))
	}
	if strings.TrimSpace(marketDir) != "" {
		dirs = append(dirs, filepath.Join(marketDir, skillID))
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
	name, description, triggers, metadata := parseSkillPromptMetadata(prompt)
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
		Name:            skillDisplayName(name, description, skillID),
		Description:     description,
		Triggers:        triggers,
		Metadata:        metadata,
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
