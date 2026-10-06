package catalog

import "fmt"

// New agents refer to shared catalog resources. Recheck the concrete client
// selection before publishing files, since the catalog can change after the
// creation dialog was opened. Imports keep their separate repair workflow.
func (r *FileRegistry) validateNewAgentReferences(definition map[string]any) error {
	def, _, err := parseAgentTree("agent candidate", cloneAgentSnapshotMap(definition))
	if err != nil {
		return err
	}
	availableTools := map[string]bool{}
	for _, id := range mergePresetConnectors(def, r.cfg.PresetsForMode(def.Mode).Connectors) {
		pkg, err := r.cfg.Paths.ConnectorSources().Load(id)
		if err != nil {
			return fmt.Errorf("connector %q is unavailable: %w", id, err)
		}
		for _, name := range pkg.NativeTools() {
			availableTools[name] = true
		}
	}
	for _, id := range def.Skills {
		if _, ok := r.SkillDefinition(id); !ok {
			return fmt.Errorf("skill %q is unavailable", id)
		}
	}
	for _, name := range def.DeclaredTools {
		if _, ok := r.Tool(name); !ok && !availableTools[name] {
			return fmt.Errorf("tool %q is unavailable", name)
		}
	}
	return nil
}
