package catalog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const manifestFile = ".market-synced-skills"

func loadSkills(root string, maxPromptChars int) (map[string]SkillDefinition, error) {
	items := map[string]SkillDefinition{}
	err := visitRuntimeEntries(
		root,
		nil,
		func(name string, entry os.DirEntry) bool {
			return entry.IsDir() && !strings.HasPrefix(name, ".") && ShouldLoadRuntimeName(name)
		},
		func(name string, _ os.DirEntry) {
			skillPath := filepath.Join(root, name, "SKILL.md")
			content, err := os.ReadFile(skillPath)
			if err != nil {
				log.Printf("[catalog][skills] skip directory %s: no SKILL.md found", name)
				return
			}
			prompt := strings.TrimSpace(string(content))
			description := firstNonEmptyMarkdownLine(prompt)
			truncated := false
			if maxPromptChars > 0 && len(prompt) > maxPromptChars {
				truncated = true
			}
			items[name] = SkillDefinition{
				Key:             name,
				Name:            skillDisplayName(description, name),
				Description:     description,
				Prompt:          prompt,
				PromptTruncated: truncated,
			}
		},
	)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func reconcileDeclaredSkills(agentDir string, declaredSkills []string, marketDir string) error {
	if strings.TrimSpace(agentDir) == "" {
		return nil
	}
	skillsDir := filepath.Join(agentDir, "skills")

	previous, _ := readManifest(skillsDir)
	declared := normalizeSkillIDs(declaredSkills)

	for _, prev := range previous {
		if _, kept := declared[prev]; kept {
			continue
		}
		target := filepath.Join(skillsDir, prev)
		if !insideDir(skillsDir, target) {
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			log.Printf("[catalog][skills] remove stale skill %s: %v", prev, err)
		}
	}

	if len(declared) == 0 {
		_ = writeManifest(skillsDir, nil)
		return nil
	}

	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create agent skills dir: %w", err)
	}

	synced := make([]string, 0, len(declared))
	for skillID := range declared {
		source := filepath.Join(marketDir, skillID)
		if !insideDir(marketDir, source) {
			log.Printf("[catalog][skills] skip skill %q: invalid path", skillID)
			continue
		}
		info, err := os.Stat(source)
		if err != nil {
			log.Printf("[catalog][skills] skip skill %q: not found in skills-market (%v)", skillID, err)
			continue
		}
		if !info.IsDir() {
			log.Printf("[catalog][skills] skip skill %q: not a directory", skillID)
			continue
		}
		target := filepath.Join(skillsDir, skillID)
		if !insideDir(skillsDir, target) {
			log.Printf("[catalog][skills] skip skill %q: target outside skills dir", skillID)
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			log.Printf("[catalog][skills] reset target for %q: %v", skillID, err)
			continue
		}
		if err := copyDirRecursive(source, target); err != nil {
			log.Printf("[catalog][skills] copy %q failed: %v", skillID, err)
			continue
		}
		synced = append(synced, skillID)
	}

	sort.Strings(synced)
	return writeManifest(skillsDir, synced)
}

func normalizeSkillIDs(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
			continue
		}
		out[id] = struct{}{}
	}
	return out
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

func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if !insideDir(dst, target) {
			return fmt.Errorf("refusing to copy outside target: %s", target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func readManifest(skillsDir string) ([]string, error) {
	path := filepath.Join(skillsDir, manifestFile)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var ids []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		id := strings.TrimSpace(scanner.Text())
		if id == "" || strings.HasPrefix(id, "#") {
			continue
		}
		ids = append(ids, id)
	}
	return ids, scanner.Err()
}

func writeManifest(skillsDir string, ids []string) error {
	if len(ids) == 0 {
		_ = os.Remove(filepath.Join(skillsDir, manifestFile))
		return nil
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(skillsDir, manifestFile)
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	if _, err := writer.WriteString("# market-synced skills (managed by agent-platform-runner)\n"); err != nil {
		file.Close()
		return err
	}
	for _, id := range ids {
		if _, err := writer.WriteString(id + "\n"); err != nil {
			file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
