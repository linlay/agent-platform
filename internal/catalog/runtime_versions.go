package catalog

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/runtimeskills"
)

func (a *runtimeAgentAssembler) installSkill(source string) (string, error) {
	before, err := runtimeskills.Digest(source)
	if err != nil {
		return "", err
	}
	root := runtimeskills.Root(a.root)
	target, err := runtimeskills.Path(root, before)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(target); err == nil {
		got, err := runtimeskills.Digest(target)
		if err != nil || got != before {
			return "", fmt.Errorf("shared Skill integrity failure: %s", before)
		}
		return before, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	candidate, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return "", err
	}
	defer runtimeskills.Remove(candidate)
	if err := copyRuntimePath(source, candidate); err != nil {
		return "", err
	}
	got, err := runtimeskills.Digest(candidate)
	if err != nil {
		return "", err
	}
	after, err := runtimeskills.Digest(source)
	if err != nil {
		return "", err
	}
	if got != before || after != before {
		return "", fmt.Errorf("Skill source changed during assembly")
	}
	if err := os.Rename(candidate, target); err != nil {
		return "", err
	}
	if err := runtimeskills.Seal(target); err != nil {
		_ = runtimeskills.Remove(target)
		return "", err
	}
	return before, nil
}

func (a *runtimeAgentAssembler) verifyVersion(dir string) error {
	expected, ok := a.contentDigests[dir]
	if !ok {
		return fmt.Errorf("unknown runtime version")
	}

	return verifyRuntimeVersionContent(dir, expected)
}

func verifyRuntimeVersionContent(dir, expected string) error {
	got, err := runtimeskills.DigestExcept(dir, ".revision", ".content-digest")
	if err != nil {
		return err
	}
	if got != expected {
		return fmt.Errorf("Agent runtime integrity failure")
	}
	return runtimeskills.Verify(dir)
}

// Called under executionMu; publication, lease acquisition and GC are serialized.
func (r *FileRegistry) collectRuntimeVersions() {
	if r.assembler == nil {
		return
	}
	keep := map[string]bool{}
	r.mu.RLock()
	for _, def := range r.agents {
		keep[def.RuntimeDir] = true
	}
	r.mu.RUnlock()
	for dir, count := range r.runtimeVersions {
		if count > 0 {
			keep[dir] = true
		}
	}
	root := r.assembler.root
	agents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, agent := range agents {
		if !agent.IsDir() || strings.HasPrefix(agent.Name(), ".") {
			continue
		}
		parent := filepath.Join(root, agent.Name())
		versions, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, version := range versions {
			dir := filepath.Join(parent, version.Name())
			if !version.IsDir() || keep[dir] {
				continue
			}
			if err := runtimeskills.Remove(dir); err != nil {
				log.Printf("[catalog][gc] %s: %v", dir, err)
				keep[dir] = true
			} else {
				delete(r.assembler.contentDigests, dir)
				delete(r.assembler.versionSkills, dir)
			}
		}
		_ = os.Remove(parent)
	}
	for dir := range r.assembler.versionSkills {
		if !keep[dir] {
			if _, err := os.Lstat(dir); os.IsNotExist(err) {
				delete(r.assembler.versionSkills, dir)
				delete(r.assembler.contentDigests, dir)
			}
		}
	}
	skillKeep := map[string]bool{}
	for dir := range keep {
		refs, known := r.assembler.versionSkills[dir]
		// GC uses trusted in-memory references, never mutable files.
		if !known {
			return
		}
		for _, ref := range refs {
			skillKeep[ref.Digest] = true
		}
	}
	skillRoot := runtimeskills.Root(root)
	entries, err := os.ReadDir(skillRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || skillKeep[entry.Name()] {
			continue
		}
		if err := runtimeskills.Remove(filepath.Join(skillRoot, entry.Name())); err != nil {
			log.Printf("[catalog][gc] Skill %s: %v", entry.Name(), err)
		}
	}
}

// Repair only unused corrupt objects. Leased paths are never replaced in place.
// New content must still pass normal source assembly; there is no historical repair.
func (r *FileRegistry) discardUnusedCorruptResources() {
	if r.assembler == nil {
		return
	}
	a := r.assembler
	liveSkills := map[string]bool{}
	for dir, count := range r.runtimeVersions {
		if count <= 0 {
			continue
		}
		refs, known := a.versionSkills[dir]
		if !known {
			return
		}
		for _, ref := range refs {
			liveSkills[ref.Digest] = true
		}
	}
	for dir, expected := range a.contentDigests {
		if r.runtimeVersions[dir] > 0 {
			continue
		}
		actual, err := runtimeskills.DigestExcept(dir, ".revision", ".content-digest")
		if err != nil || actual != expected {
			if err := runtimeskills.Remove(dir); err != nil {
				log.Printf("[catalog][runtime] cannot discard corrupt version: %v", err)
				continue
			}
			delete(a.contentDigests, dir)
			// Keep trusted Skill references until this published definition is
			// replaced: a failed rebuild may retain it for diagnostics/GC.
		}
	}
	root := runtimeskills.Root(a.root)
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || liveSkills[entry.Name()] {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		digest, err := runtimeskills.Digest(dir)
		if err != nil || digest != entry.Name() {
			_ = runtimeskills.Remove(dir)
		}
	}
}

func (r *FileRegistry) updateRuntimeDiagnostics() {
	if r.assembler == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, item := range r.adminAgents {
		if item.Meta == nil {
			item.Meta = map[string]any{}
		}
		versions := map[string]bool{}
		if def, ok := r.agents[key]; ok {
			versions[def.RuntimeDir] = true
			item.Meta["runtimeRevision"] = def.RuntimeRevision
		}
		for dir, n := range r.runtimeVersions {
			if n > 0 && filepath.Dir(dir) == filepath.Join(r.assembler.root, key) {
				versions[dir] = true
			}
		}
		item.Meta["runtimeRetainedVersions"] = len(versions)
		item.Meta["runtimeLeaseCount"] = r.runtimeUsers[key]
		r.adminAgents[key] = item
	}
}

func runtimeSourceDigest(source EditableAgentSource) (string, error) {
	if source.Kind == "directory" {
		return runtimeskills.DigestExcept(source.AgentDir, "skills", "connectors")
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
