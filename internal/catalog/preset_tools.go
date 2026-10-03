package catalog

import (
	"fmt"
	"slices"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/connector"
)

func validatePresetTools(names []string, definitions []api.ToolDetailResponse) error {
	registered := map[string]bool{}
	for _, def := range definitions {
		source, _ := def.Meta["sourceType"].(string)
		registered[def.Name] = source != "mcp"
	}
	for _, name := range names {
		if !registered[name] {
			return fmt.Errorf("preset-tools: tool %q is not registered locally", name)
		}
		_, native := connector.NativeToolConnector(name)
		if strings.HasPrefix(name, "_") || name == "agent_delegate" || native {
			return fmt.Errorf("preset-tools: tool %q requires an internal session or connector mount", name)
		}
	}
	return nil
}

func (d *AgentDefinition) applyPresetTools(presets []string) {
	if d.Engine == AgentEngineACP || (d.Mode != AgentModeGeneral && d.Mode != AgentModeCoder && d.Mode != AgentModeKBase) {
		return
	}
	if d.DeclaredTools == nil {
		d.DeclaredTools = append([]string{}, d.Tools...)
	}
	base := applyAgentModeProfileDefaults(AgentDefinition{Mode: d.Mode, ACPBridgeID: d.ACPBridgeID, Tools: append([]string{}, d.DeclaredTools...)})
	d.Tools = nil
	d.ToolBindings = nil
	excluded := map[string]bool{}
	for _, name := range d.ExcludedTools {
		excluded[name] = true
	}
	seen := map[string]bool{}
	for _, group := range []struct {
		names  []string
		source string
	}{{presets, "preset"}, {base.Tools, "agent"}} {
		for _, name := range group.names {
			if seen[name] {
				continue
			}
			seen[name] = true
			active := !excluded[name]
			d.ToolBindings = append(d.ToolBindings, api.AgentToolBinding{Name: name, Source: group.source, Removable: group.source == "agent", Excluded: excluded[name], Active: active})
			if active {
				d.Tools = append(d.Tools, name)
			}
		}
	}
}

// Connector/runtime dependencies can reintroduce an excluded base tool. Excluded
// describes the YAML exclusion; Active describes the effective capability.
func (d *AgentDefinition) addAutomaticToolBinding(name, source string) {
	for i := range d.ToolBindings {
		if d.ToolBindings[i].Name == name {
			d.ToolBindings[i].Active = true
			d.ToolBindings[i].Removable = false
			if d.ToolBindings[i].Source != "preset" {
				d.ToolBindings[i].Source = source
			}
			return
		}
	}
	d.ToolBindings = append(d.ToolBindings, api.AgentToolBinding{Name: name, Source: source, Active: true})
}

func (d *AgentDefinition) finishToolBindings() {
	if d.Engine == AgentEngineACP || (d.Mode != AgentModeGeneral && d.Mode != AgentModeCoder && d.Mode != AgentModeKBase) {
		return
	}
	derived := applyKBaseCapabilityTools(AgentDefinition{KBaseConfig: d.KBaseConfig})
	// run_env is mounted by default, independent of declarations and presets,
	// but still respects excludeTools. Child/Team sessions hide it separately.
	derived.Tools = append(derived.Tools, "run_env")
	if len(d.Skills) > 0 || runtimeRequiresBash(d.Runtime) {
		derived.Tools = append(derived.Tools, "bash")
	}
	if d.MemoryEnabled {
		derived.Tools = append(derived.Tools, "memory_write", "memory_read", "memory_search")
	}
	for _, name := range derived.Tools {
		d.addAutomaticToolBinding(name, "runtime")
		if name == "run_env" && containsString(d.ExcludedTools, name) {
			d.Tools = slices.DeleteFunc(d.Tools, func(tool string) bool { return tool == name })
			for i := range d.ToolBindings {
				if d.ToolBindings[i].Name == name {
					d.ToolBindings[i].Active = false
					d.ToolBindings[i].Excluded = true
				}
			}
			continue
		}
		if !containsString(d.Tools, name) {
			d.Tools = append(d.Tools, name)
		}
	}
}

func (d AgentDefinition) EffectiveToolBindings() []api.AgentToolBinding {
	result := append([]api.AgentToolBinding{}, d.ToolBindings...)
	seen := map[string]bool{}
	for i := range result {
		result[i].Active = containsString(d.Tools, result[i].Name)
		seen[result[i].Name] = true
	}
	for _, name := range d.Tools {
		if !seen[name] {
			result = append(result, api.AgentToolBinding{Name: name, Source: "agent", Removable: true, Active: true})
			seen[name] = true
		}
	}
	return result
}

// stripPresetToolDeclarations is used only by structured create/update, never
// source editing. Fixed native tools and presets need no new declaration;
// existing source declarations are preserved.
func stripPresetToolDeclarations(definition map[string]any, presets, existing []string) {
	if stringNode(definition["engine"]) == AgentEngineACP {
		return
	}
	preset, keep := map[string]bool{}, map[string]bool{}
	switch strings.ToUpper(strings.TrimSpace(stringNode(definition["mode"]))) {
	case "", "REACT", AgentModeGeneral, AgentModeCoder, AgentModeKBase:
		preset["run_env"] = true
	}
	for _, name := range presets {
		preset[name] = true
	}
	for _, name := range existing {
		keep[name] = true
	}
	config := mapNode(definition["toolConfig"])
	names := listStrings(config["tools"])
	result := []string{}
	for _, name := range names {
		if !preset[name] || keep[name] {
			result = append(result, name)
		}
	}
	// Locked existing declarations must not be removed by a form omitting presets.
	for _, name := range existing {
		if preset[name] && !containsString(result, name) {
			result = append(result, name)
		}
	}
	if config != nil || len(result) > 0 {
		if config == nil {
			config = map[string]any{}
		}
		config["tools"] = result
		definition["toolConfig"] = config
	}
}
