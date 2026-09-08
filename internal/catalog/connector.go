package catalog

import (
	"fmt"
	"path/filepath"
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

// ConnectorMount freezes the shared package runtime path for this Agent snapshot.
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
		pkg, err := a.connectors.LoadRuntime(id)
		if err != nil {
			return err
		}
		if (pkg.CLI != nil || len(pkg.Skills) > 0) && !strings.EqualFold(def.Mode, AgentModeKBase) && !containsString(def.Tools, "bash") {
			def.Tools = append(def.Tools, "bash")
		}
		if pkg.BinDir != "" {
			def.ConnectorBinDirs = append(def.ConnectorBinDirs, pkg.BinDir)
		}
		def.ConnectorMounts = append(def.ConnectorMounts, ConnectorMount{ID: id, Dir: pkg.Dir})
		def.ConnectorMCPServers = append(def.ConnectorMCPServers, pkg.ServerKeys()...)
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

// ResolveSkillDefinition resolves shared connector skills from the mounted
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
