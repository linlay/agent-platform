package catalog

import (
	"fmt"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
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
		owner, native := connector.NativeToolConnector(name)
		if native {
			return fmt.Errorf("preset-tools: tool %q belongs to connector %s; list it under preset-connectors", name, owner)
		}
		if contracts.IsMCPGatewayToolName(name) {
			return fmt.Errorf("preset-tools: tool %q is offered by the platform when tools.yml mcp.tool-mode is true", name)
		}
		if strings.HasPrefix(name, "_") || name == "agent_delegate" {
			return fmt.Errorf("preset-tools: tool %q requires an internal session or connector mount", name)
		}
	}
	return nil
}

func (d *AgentDefinition) applyPresetTools(presets []string) {
	if d.Engine == AgentEngineACP || !presetMode(d.Mode) {
		return
	}
	if d.DeclaredTools == nil {
		d.DeclaredTools = append([]string{}, d.Tools...)
	}

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
	}{{presets, "preset"}, {d.DeclaredTools, "agent"}} {
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

// toolConfig.excludeTools also removes a mounted native connector's tool; the
// binding stays listed so the exclusion remains visible.
func (d *AgentDefinition) addConnectorToolBinding(name string, excluded bool) {
	for i := range d.ToolBindings {
		if d.ToolBindings[i].Name == name {
			d.ToolBindings[i].Active = !excluded
			d.ToolBindings[i].Excluded = excluded
			d.ToolBindings[i].Removable = false
			if d.ToolBindings[i].Source != "preset" {
				d.ToolBindings[i].Source = "connector"
			}
			return
		}
	}
	d.ToolBindings = append(d.ToolBindings, api.AgentToolBinding{Name: name, Source: "connector", Excluded: excluded, Active: !excluded})
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

// presetMode reports whether an Agent mode receives the agent-settings presets:
// the ordinary native modes, never pipeline or remote execution modes.
func presetMode(mode string) bool {
	switch mode {
	case AgentModeGeneral, AgentModeCoder, AgentModeKBase, AgentModeExecutor, "TEAM":
		return true
	}
	return false
}
