package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
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
	ID     string
	Dir    string
	Digest string
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
	return resolveConnectorPackages(def, func(id string) (connector.Package, error) {
		pkg, err := a.connectors.Load(id)
		if err != nil {
			return pkg, err
		}
		return a.connectors.InstallShared(pkg)
	})
}

func resolveConnectorPackages(def *AgentDefinition, load func(string) (connector.Package, error)) error {
	def.ConnectorNativeTools = nil
	def.ConnectorSkills = nil
	def.ConnectorBinDirs = nil
	def.ConnectorEnv = map[string]string{}
	def.ConnectorCredentials = nil
	credentialNames := map[string]bool{}
	def.ConnectorCLIEntries = nil
	def.ConnectorMounts = nil
	def.ConnectorMCPServers = nil
	if (containsString(def.Tools, "desktop_action") || containsString(def.Tools, "desktop_cdp")) && !containsString(def.Connectors, "builtin.desktop") {
		return fmt.Errorf("Desktop tools require connectorConfig.connectors: [builtin.desktop]; migrate legacy tool configuration")
	}
	skillSources := make(map[string]string, len(def.Skills))
	for _, key := range def.Skills {
		if connector.IsReservedSkill(key) {
			return fmt.Errorf("connector skills are imported by connectorConfig.connectors, not skillConfig.skills")
		}
		skillSources[strings.ToLower(strings.TrimSpace(key))] = "skillConfig.skills"
	}
	for _, id := range def.Connectors {
		pkg, err := load(id)
		if err != nil {
			return err
		}
		if pkg.Type == "native" {
			if !pkg.Builtin {
				return fmt.Errorf("native capabilities require a trusted builtin source")
			}
			if strings.EqualFold(def.Mode, AgentModeKBase) {
				return fmt.Errorf("Desktop connector is unavailable in KBASE mode")
			}
			def.ConnectorNativeTools = append(def.ConnectorNativeTools, pkg.NativeTools()...)
			for _, tool := range append(pkg.NativeTools(), "file_read") {
				if !containsString(def.Tools, tool) {
					def.Tools = append(def.Tools, tool)
				}
			}
		}
		values, err := pkg.CLIConfigEnvironment()
		if err != nil {
			return err
		}
		if err := connectorauth.ValidatePackage(pkg); err != nil {
			return err
		}
		credential, err := connectorauth.CLIEnvironment(pkg)
		if err != nil {
			return err
		}
		if len(credential.Env) > 0 {
			for name := range credential.Env {
				if credentialNames[name] {
					return fmt.Errorf("connectors have conflicting credential env %s", name)
				}
				credentialNames[name] = true
			}
			def.ConnectorCredentials = append(def.ConnectorCredentials, credential)
		}
		for key, value := range values {
			if previous, exists := def.ConnectorEnv[key]; exists && previous != value {
				return fmt.Errorf("connectors have conflicting configEnv %s", key)
			}
			def.ConnectorEnv[key] = value
		}
		if (pkg.CLI != nil || len(pkg.Skills) > 0 && pkg.Type != "native") && !strings.EqualFold(def.Mode, AgentModeKBase) && !containsString(def.Tools, "bash") {
			def.Tools = append(def.Tools, "bash")
		}
		if pkg.BinDir != "" && (pkg.CLI != nil || len(pkg.MCP) > 0 || len(pkg.Skills) > 0) {
			def.ConnectorBinDirs = append(def.ConnectorBinDirs, pkg.BinDir)
		}
		def.ConnectorMounts = append(def.ConnectorMounts, ConnectorMount{ID: id, Dir: pkg.Dir, Digest: filepath.Base(pkg.Dir)})
		for _, component := range pkg.ServerKeys() {
			def.ConnectorMCPServers = append(def.ConnectorMCPServers, connector.AgentVersionServerKey(def.Key, component, filepath.Base(pkg.Dir)))
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
	binDirs := make(map[string]bool, len(d.ConnectorBinDirs))
	for _, dir := range d.ConnectorBinDirs {
		binDirs[filepath.Clean(dir)] = true
	}
	d.ConnectorBinDirs = nil
	d.ConnectorCLIEntries = nil
	for i := range d.ConnectorMounts {
		mount := &d.ConnectorMounts[i]
		hasExecutableComponent := binDirs[filepath.Join(mount.Dir, "bin")]
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
			result = append(result, connector.AgentRuntime{AgentKey: def.Key, ID: mount.ID, Dir: mount.Dir, Digest: mount.Digest})
		}
	}
	if r.assembler != nil {
		if pins, err := r.assembler.connectors.PinnedRuntimes(); err == nil {
			result = append(result, pins...)
		}
	}
	for _, mount := range r.liveConnectorMounts {
		result = append(result, mount)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AgentKey != result[j].AgentKey {
			return result[i].AgentKey < result[j].AgentKey
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (r *FileRegistry) reconcileSharedPins() error {
	if r.sharedPins == nil {
		r.sharedPins = map[string]func(){}
	}
	wanted := map[string]bool{}
	for _, mount := range r.ConnectorRuntimes() {
		wanted[mount.Dir] = true
		if r.sharedPins[mount.Dir] == nil {
			release, err := connector.RetainShared(mount.Dir)
			if err != nil {
				return err
			}
			r.sharedPins[mount.Dir] = release
		}
	}
	for dir, release := range r.sharedPins {
		if !wanted[dir] {
			release()
			delete(r.sharedPins, dir)
		}
	}
	return nil
}
