package catalog

import "strings"

// PresetConnectorIDs describes platform configuration, independently of the
// published runtime snapshot (which may still be leased by a Run).
func (r *FileRegistry) PresetConnectorIDs(key string) ([]string, error) {
	source, err := r.ReadEditableAgentSource(key)
	if err != nil {
		return nil, err
	}
	tree, _, err := agentConnectorTree(source.Content)
	if err != nil {
		return nil, err
	}
	return presetConnectorIDsForTree(tree, r.cfg.PresetsForMode(stringNode(tree["mode"])).Connectors), nil
}

// Configuration display must remain available for an otherwise invalid Agent
// (for example one with a missing model or workspace).
func presetConnectorIDsForTree(tree map[string]any, presets []string) []string {
	engine := strings.TrimSpace(stringNode(tree["engine"]))
	if engine != "" && engine != AgentEngineNative {
		return []string{}
	}
	mode := strings.ToUpper(strings.TrimSpace(stringNode(tree["mode"])))
	if mode == "" {
		mode = AgentModeGeneral
	}
	return mergePresetConnectors(AgentDefinition{Engine: AgentEngineNative, Mode: mode}, presets)
}

func mergePresetConnectors(def AgentDefinition, presets []string) []string {
	result := []string{}
	if def.Engine != AgentEngineACP && (def.Mode == AgentModeGeneral || def.Mode == AgentModeCoder || def.Mode == AgentModeKBase) {
		for _, id := range presets {
			if !containsString(result, id) {
				result = append(result, id)
			}
		}
	}
	for _, id := range def.Connectors {
		if !containsString(result, id) {
			result = append(result, id)
		}
	}
	return result
}

// Structured editors must not turn inherited connectors into source declarations.
// Preserve explicit references already present in the source.
func stripPresetConnectorDeclarations(definition map[string]any, presets, existing []string) {
	def, _, err := parseAgentTree("agent.yml", cloneAgentSnapshotMap(definition))
	if err != nil || def.Engine == AgentEngineACP {
		return
	}
	def.Connectors = nil
	presets = mergePresetConnectors(def, presets)
	section := mapNode(definition["connectorConfig"])
	if section == nil {
		section = map[string]any{}
	}
	result := []string{}
	for _, id := range listStrings(section["connectors"]) {
		if !containsString(presets, id) || containsString(existing, id) {
			result = append(result, id)
		}
	}
	for _, id := range existing {
		if containsString(presets, id) && !containsString(result, id) {
			result = append(result, id)
		}
	}
	if len(result) > 0 || definition["connectorConfig"] != nil {
		section["connectors"] = result
		definition["connectorConfig"] = section
	}
}

// EffectiveConnectorIDs validates an Agent definition and merges platform mounts
// before connector selection validation, including catalog tool candidate edits.
func EffectiveConnectorIDs(definition map[string]any, presets []string) ([]string, error) {
	def, _, err := parseAgentTree("agent.yml", cloneAgentSnapshotMap(definition))
	if err != nil {
		return nil, err
	}
	return mergePresetConnectors(def, presets), nil
}
