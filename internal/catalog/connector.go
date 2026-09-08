package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/connector"
)

type ConnectorSkill struct {
	// Key is the original skill name; ConnectorID records its source separately.
	Key         string
	ConnectorID string
	Name        string
	RuntimeDir  string
}

// ConnectorMount freezes the package path in this Agent execution directory.
type ConnectorMount struct {
	ID  string
	Dir string
}

func (d AgentDefinition) EffectiveSkills() []string {
	keys := append([]string(nil), d.Skills...)
	for _, skill := range d.ConnectorSkills {
		keys = append(keys, skill.Key)
	}
	return keys
}

func (d AgentDefinition) IsConnectorSkill(key string) bool {
	for _, skill := range d.ConnectorSkills {
		if strings.EqualFold(skill.Key, key) {
			return true
		}
	}
	return false
}

func parseConnectorIDs(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("connectorConfig.connectors must be an array")
	}
	var result []string
	seen := map[string]bool{}
	for _, item := range items {
		id, ok := item.(string)
		if !ok || !connector.ValidID(id) {
			return nil, fmt.Errorf("connectorConfig.connectors contains invalid connector id %q", item)
		}
		if !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result, nil
}

func (a *runtimeAgentAssembler) resolveConnectors(def *AgentDefinition) error {
	def.ConnectorSkills = nil
	def.ConnectorBinDirs = nil
	def.ConnectorCLIEntries = nil
	def.ConnectorMounts = nil
	def.ConnectorMCPServers = nil
	skillSources := make(map[string]string, len(def.Skills))
	for _, key := range def.Skills {
		if connector.IsReservedSkill(key) {
			return fmt.Errorf("connector skills are imported by connectorConfig.connectors, not skillConfig.skills")
		}
		skillSources[strings.ToLower(strings.TrimSpace(key))] = "skillConfig.skills"
	}
	for _, id := range def.Connectors {
		pkg, err := a.connectors.Load(id)
		if err != nil {
			return err
		}
		if (pkg.CLI != nil || len(pkg.Skills) > 0) && !strings.EqualFold(def.Mode, AgentModeKBase) && !containsString(def.Tools, "bash") {
			def.Tools = append(def.Tools, "bash")
		}
		if pkg.BinDir != "" && (pkg.CLI != nil || len(pkg.MCP) > 0 || len(pkg.Skills) > 0) {
			def.ConnectorBinDirs = append(def.ConnectorBinDirs, pkg.BinDir)
		}
		def.ConnectorMounts = append(def.ConnectorMounts, ConnectorMount{ID: id, Dir: pkg.Dir})
		for _, component := range pkg.ServerKeys() {
			def.ConnectorMCPServers = append(def.ConnectorMCPServers, connector.AgentServerKey(def.Key, component))
		}
		for _, skill := range pkg.Skills {
			key := skill.Name
			if source, exists := skillSources[key]; exists {
				return fmt.Errorf("skill %q from connector %q conflicts with %s; skill names must be unique within an Agent", key, id, source)
			}
			skillSources[key] = fmt.Sprintf("connector %q", id)
			def.ConnectorSkills = append(def.ConnectorSkills, ConnectorSkill{Key: key, ConnectorID: id, Name: skill.Name, RuntimeDir: skill.Dir})
		}
	}
	return nil
}

func (a *runtimeAgentAssembler) resolveEffectiveSkillSource(source EditableAgentSource, def AgentDefinition, key string) (string, error) {
	for _, skill := range def.ConnectorSkills {
		if skill.Key == key {
			return skill.RuntimeDir, nil
		}
	}
	return a.resolveSkillSource(source, key)
}

func (d AgentDefinition) ConnectorRuntimeSkillDirs() []string {
	var result []string
	for _, skill := range d.ConnectorSkills {
		result = append(result, skill.RuntimeDir)
	}
	return result
}

// ResolveSkillDefinition resolves connector skills from this Agent's mounted
// runtime package and ordinary skills from this Agent's generated directory.
func (d AgentDefinition) ResolveSkillDefinition(key string) (SkillDefinition, bool, error) {
	for _, skill := range d.ConnectorSkills {
		if skill.Key == key {
			return loadSkillDefinitionFromDir(skill.RuntimeDir, key, 0)
		}
	}
	return ResolveRuntimeSkillDefinition(d.RuntimeDir, key)
}

func (d AgentDefinition) SkillInstructionsPath(key string) string {
	for _, skill := range d.ConnectorSkills {
		if skill.Key != key {
			continue
		}
		for _, mount := range d.ConnectorMounts {
			if mount.ID == skill.ConnectorID {
				rel, err := filepath.Rel(mount.Dir, skill.RuntimeDir)
				if err == nil {
					return "@connectors/" + mount.ID + "/" + filepath.ToSlash(rel) + "/SKILL.md"
				}
			}
		}
	}
	return "@skills/" + key + "/SKILL.md"
}

// bindConnectorRuntime converts source metadata to stable Agent-local paths.
func (d *AgentDefinition) bindConnectorRuntime() error {
	root := filepath.Join(d.RuntimeDir, "connectors")
	for i := range d.ConnectorSkills {
		skill := &d.ConnectorSkills[i]
		for _, mount := range d.ConnectorMounts {
			if mount.ID == skill.ConnectorID {
				rel, _ := filepath.Rel(mount.Dir, skill.RuntimeDir)
				skill.RuntimeDir = filepath.Join(root, mount.ID, rel)
				break
			}
		}
	}
	binDirs := make(map[string]bool, len(d.ConnectorBinDirs))
	for _, dir := range d.ConnectorBinDirs {
		binDirs[filepath.Clean(dir)] = true
	}
	d.ConnectorBinDirs = nil
	d.ConnectorCLIEntries = nil
	for i := range d.ConnectorMounts {
		mount := &d.ConnectorMounts[i]
		hasExecutableComponent := binDirs[filepath.Join(mount.Dir, "bin")]
		mount.Dir = filepath.Join(root, mount.ID)
		if info, err := os.Stat(filepath.Join(mount.Dir, "bin")); hasExecutableComponent && err == nil && info.IsDir() {
			d.ConnectorBinDirs = append(d.ConnectorBinDirs, filepath.Join(mount.Dir, "bin"))
			entries, err := connector.SnapshotCLIEntries(mount.ID, mount.Dir)
			if err != nil {
				return err
			}
			d.ConnectorCLIEntries = append(d.ConnectorCLIEntries, entries...)
		}
	}
	return nil
}

func (r *FileRegistry) ConnectorRuntimes() []connector.AgentRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []connector.AgentRuntime
	for _, def := range r.agents {
		for _, mount := range def.ConnectorMounts {
			result = append(result, connector.AgentRuntime{AgentKey: def.Key, ID: mount.ID, Dir: mount.Dir})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AgentKey != result[j].AgentKey {
			return result[i].AgentKey < result[j].AgentKey
		}
		return result[i].ID < result[j].ID
	})
	return result
}
