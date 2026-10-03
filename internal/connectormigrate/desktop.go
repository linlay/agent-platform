package connectormigrate

import (
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DesktopChange struct {
	Path       string   `json:"path"`
	AddedTools []string `json:"addedTools,omitempty"`
	Before     string   `json:"before"`
	After      string   `json:"after"`
	Data       []byte   `json:"-"`
}
type DesktopReview struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	Reason string `json:"reason"`
}
type DesktopPlan struct {
	Pending      []DesktopReview `json:"pending,omitempty"`
	Root         string          `json:"root"`
	Changes      []DesktopChange `json:"changes"`
	RetireSkills []string        `json:"retireSkills"`
	Backup       string          `json:"backup,omitempty"`
}

func desktopList(root map[string]any, section, key string) ([]string, error) {
	m, _ := root[section].(map[string]any)
	if root[section] != nil && m == nil {
		return nil, fmt.Errorf("%s must be a map", section)
	}
	var values []any
	switch value := m[key].(type) {
	case nil:
	case []any:
		values = value
	case string:
		values = []any{value}
	case map[string]any:
		// A key with no value ("skills:") loads as an empty mapping.
		if len(value) != 0 {
			return nil, fmt.Errorf("invalid %s.%s", section, key)
		}
	default:
		return nil, fmt.Errorf("invalid %s.%s", section, key)
	}
	result := []string{}
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid list value")
		}
		result = append(result, s)
	}
	return result, nil
}
func sum(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// PreviewDesktop preserves all unrelated YAML bytes, including secrets and prompts.
func PreviewDesktop(runtimeRoot string) (DesktopPlan, error) {
	root, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return DesktopPlan{}, err
	}
	plan := DesktopPlan{Root: root, Changes: []DesktopChange{}, RetireSkills: []string{}}
	err = filepath.WalkDir(filepath.Join(root, "agents"), func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration does not follow symlinks: %s", path)
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(root, "agents"), path)
		if filepath.Dir(rel) != "." && filepath.Base(path) != "agent.yml" && filepath.Base(path) != "agent.yaml" {
			return nil
		}
		if filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree, err := config.LoadYAMLTreeBytes(data)
		if err != nil {
			return err
		}
		node, ok := tree.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid Agent %s", path)
		}
		tools, err := desktopList(node, "toolConfig", "tools")
		if err != nil {
			return err
		}
		skills, err := desktopList(node, "skillConfig", "skills")
		if err != nil {
			return err
		}
		mounts, err := desktopList(node, "connectorConfig", "connectors")
		if err != nil {
			return err
		}

		found, needControl, needWeb := false, false, false
		legacyDesktop := false
		keptTools, keptSkills := []string{}, []string{}
		for _, tool := range tools {
			switch tool {
			case "platform_control":
				found = true
			case "desktop_action":
				found = true
				needControl = true
			case "desktop_cdp":
				found = true
				needWeb = true
			default:
				keptTools = append(keptTools, tool)
			}
		}
		for _, skill := range skills {
			switch skill {
			case "desktop-action":
				found = true
				needControl = true
			case "desktop-cdp":
				found = true
				needWeb = true
			default:
				keptSkills = append(keptSkills, skill)
			}
		}
		unique := []string{}
		seen := map[string]bool{}
		for _, id := range mounts {
			switch id {
			case "builtin.desktop":
				legacyDesktop = true
				id = connector.PlatformControlConnectorID
				found = true
				needControl = true
			case "builtin.desktop-web":
				id = connector.WebControlConnectorID
				found = true
				needWeb = true
			}
			if !seen[id] {
				unique = append(unique, id)
				seen[id] = true
			}
		}
		if !found {
			return nil
		}
		if strings.EqualFold(fmt.Sprint(node["engine"]), "acp") {
			return fmt.Errorf("%s cannot mount native tools with ACP", path)
		}
		change := DesktopChange{Path: path, Before: sum(data)}
		// Legacy names are not evidence of an intentional grant of all management tools.
		if legacyDesktop || needControl && !seen[connector.PlatformControlConnectorID] {
			plan.Pending = append(plan.Pending, DesktopReview{Path: path, Before: sum(data), Reason: "review this Agent: remove retired desktop declarations and explicitly configure any required platform/web connectors; no permissions are inferred"})
			return nil
		}
		if needWeb && !seen[connector.WebControlConnectorID] {
			unique = append(unique, connector.WebControlConnectorID)
		}

		for _, part := range []struct {
			section, key string
			values       []string
		}{{"toolConfig", "tools", keptTools}, {"skillConfig", "skills", keptSkills}, {"connectorConfig", "connectors", unique}} {
			data, err = replaceYAMLList(data, part.section, part.key, part.values)
			if err != nil {
				return err
			}
		}
		change.Data = data
		change.After = sum(data)
		plan.Changes = append(plan.Changes, change)
		return nil
	})
	if err != nil {
		return plan, err
	}
	if len(plan.Pending) > 0 {
		return plan, nil
	}
	for _, name := range []string{"desktop-action", "desktop-cdp"} {
		path := filepath.Join(root, "skills-center", name)
		if _, err := os.Lstat(path); err == nil {
			plan.RetireSkills = append(plan.RetireSkills, path)
		} else if !os.IsNotExist(err) {
			return plan, err
		}
	}
	return plan, nil
}

type desktopJournal struct {
	Plan        DesktopPlan       `json:"plan"`
	SkillHashes map[string]string `json:"skillHashes"`
}

// ApplyDesktop is an explicitly offline transaction; no server calls are made.
func ApplyDesktop(plan DesktopPlan, allowExpansion bool) (DesktopPlan, error) {
	for _, change := range plan.Changes {
		if len(change.AddedTools) > 0 && !allowExpansion {
			return plan, fmt.Errorf("migration adds tools for %s; review addedTools and use --allow-expansion", change.Path)
		}
	}
	release, err := connector.AcquireOperation(filepath.Join(plan.Root, ".state"), "desktop-migration")
	if err != nil {
		return plan, err
	}
	defer release()
	for _, change := range plan.Changes {
		data, err := os.ReadFile(change.Path)
		if err != nil || sum(data) != change.Before {
			return plan, fmt.Errorf("Agent changed since preview: %s", change.Path)
		}
	}
	if len(plan.Changes) == 0 && len(plan.RetireSkills) == 0 {
		return plan, nil
	}
	backup, err := os.MkdirTemp(plan.Root, ".desktop-migration-")
	if err != nil {
		return plan, err
	}
	plan.Backup = backup
	journal := desktopJournal{Plan: plan, SkillHashes: map[string]string{}}
	for i, change := range plan.Changes {
		data, err := os.ReadFile(change.Path)
		if err != nil {
			return plan, err
		}
		if err := os.WriteFile(filepath.Join(backup, fmt.Sprintf("agent-%d", i)), data, 0600); err != nil {
			return plan, err
		}
	}
	for _, path := range plan.RetireSkills {
		hash, err := connector.RuntimeFingerprint(path)
		if err != nil {
			return plan, err
		}
		journal.SkillHashes[path] = hash
	}
	if err := writeJSON(filepath.Join(backup, "journal.json"), journal, 0600); err != nil {
		return plan, err
	}
	rollback := func(cause error) (DesktopPlan, error) {
		if err := RollbackDesktop(backup); err != nil {
			return plan, fmt.Errorf("%w; rollback failed: %v", cause, err)
		}
		return plan, cause
	}
	for _, change := range plan.Changes {
		if err := desktopAtomicWrite(change.Path, change.Data); err != nil {
			return rollback(err)
		}
	}
	for i, path := range plan.RetireSkills {
		if err := os.Rename(path, filepath.Join(backup, fmt.Sprintf("skill-%d", i))); err != nil {
			return rollback(err)
		}
	}
	return plan, nil
}
func desktopAtomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".desktop-migration-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func RollbackDesktop(backup string) error {
	var journal desktopJournal
	if err := connector.ReadJSON(filepath.Join(backup, "journal.json"), &journal); err != nil {
		return err
	}
	// Check every destination before restoring anything; do not overwrite subsequent edits.
	for _, change := range journal.Plan.Changes {
		data, err := os.ReadFile(change.Path)
		if err != nil {
			return err
		}
		if sum(data) != change.Before && sum(data) != change.After {
			return fmt.Errorf("rollback would overwrite changes: %s", change.Path)
		}
	}
	for i, path := range journal.Plan.RetireSkills {
		stored := filepath.Join(backup, fmt.Sprintf("skill-%d", i))
		if _, err := os.Stat(stored); os.IsNotExist(err) {
			continue
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("rollback destination exists: %s", path)
		}
		hash, err := connector.RuntimeFingerprint(stored)
		if err != nil || hash != journal.SkillHashes[path] {
			return fmt.Errorf("skill backup changed: %s", path)
		}
	}
	for i, change := range journal.Plan.Changes {
		data, err := os.ReadFile(filepath.Join(backup, fmt.Sprintf("agent-%d", i)))
		if err != nil || sum(data) != change.Before {
			return fmt.Errorf("invalid Agent backup")
		}
		if err := desktopAtomicWrite(change.Path, data); err != nil {
			return err
		}
	}
	for i, path := range journal.Plan.RetireSkills {
		stored := filepath.Join(backup, fmt.Sprintf("skill-%d", i))
		if _, err := os.Stat(stored); err == nil {
			if err := os.Rename(stored, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func containsMigrationString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
