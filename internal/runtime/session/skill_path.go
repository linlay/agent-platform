package session

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/pathutil"
)

// ResolveSkillPathAppend returns the directories that configured skills add to
// the tool PATH, in skill configuration order and without duplicates. A
// directory must lie inside the skill's own canonical tree or under an
// administrator-approved root; entries already on the host PATH are dropped
// because the merged PATH keeps them in their original position.
func ResolveSkillPathAppend(def catalog.AgentDefinition, skillIDs []string, adminRoots []string) []string {
	hostPath := filepath.SplitList(os.Getenv("PATH"))
	seen := map[string]bool{}
	seenSkill := map[string]bool{}
	var out []string
	for _, raw := range skillIDs {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" || seenSkill[key] || def.IsConnectorSkill(key) {
			continue
		}
		seenSkill[key] = true
		skill, ok, err := def.ResolveSkillDefinition(key)
		if err != nil || !ok || len(skill.PathAppend) == 0 {
			continue
		}
		skillRoot, err := pathutil.Canonicalize(skill.Dir)
		if err != nil {
			continue
		}
		for _, dir := range skill.PathAppend {
			canonical, err := pathutil.Canonicalize(dir)
			if err != nil || seen[canonical.Key] {
				continue
			}
			if onPath(canonical, hostPath) {
				seen[canonical.Key] = true
				continue
			}
			if !pathutil.WithinRoot(canonical, skillRoot) && !underAny(canonical, adminRoots) {
				log.Printf("[server][skill-runtime][warn] skill=%s PATH entry %s ignored: outside the skill and bash.path-append-roots", key, dir)
				continue
			}
			if info, err := os.Stat(canonical.Host); err != nil || !info.IsDir() {
				continue
			}
			seen[canonical.Key] = true
			out = append(out, canonical.Host)
		}
	}
	return out
}

func onPath(dir pathutil.Canonical, path []string) bool {
	for _, entry := range path {
		if entry == "" {
			continue
		}
		if c, err := pathutil.Canonicalize(entry); err == nil && c.Key == dir.Key {
			return true
		}
	}
	return false
}

func underAny(dir pathutil.Canonical, roots []string) bool {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if c, err := pathutil.Canonicalize(root); err == nil && pathutil.WithinRoot(dir, c) {
			return true
		}
	}
	return false
}
