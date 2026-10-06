package config

import (
	"fmt"
	"strings"

	"agent-platform/internal/connector"
)

var retiredAgentFiles = []string{"general-settings.yml", "coder-settings.yml", "kbase-settings.yml", "prompts.yml", "coder-prompts.yml", "kbase-prompts.yml", "ai-tools.yml"}

func configMap(raw any, path string, keys ...string) (map[string]any, error) {
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", path)
	}
	for key := range values {
		allowed := false
		for _, candidate := range keys {
			if key == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("unknown or retired field %s.%s", path, key)
		}
	}
	return values, nil
}
func optionalConfigMap(values map[string]any, key, path string, keys ...string) (map[string]any, error) {
	raw, exists := values[key]
	if !exists {
		return map[string]any{}, nil
	}
	return configMap(raw, path+"."+key, keys...)
}
func configText(values map[string]any, key, path string) (string, error) {
	raw, exists := values[key]
	if !exists || raw == nil {
		return "", nil
	}
	if empty, ok := raw.(map[string]any); ok && len(empty) == 0 {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s must be a string", path, key)
	}
	return value, nil
}
func parseAgentPresets(values map[string]any, path string) (AgentPresets, error) {
	var out AgentPresets
	for _, key := range []string{"preset-tools", "preset-connectors"} {
		if raw, exists := values[key]; exists {
			names, err := ParseToolNames(raw, path+"."+key)
			if err != nil {
				return out, err
			}
			if key == "preset-tools" {
				out.Tools = names
			} else {
				for _, id := range names {
					if !connector.ValidID(id) {
						return out, fmt.Errorf("%s.%s: invalid connector id %q", path, key, id)
					}
				}
				out.Connectors = names
			}
		}
	}
	return out, nil
}
func parseAgentDefaults(values map[string]any, path string) (CoderDefaultAgentConfig, error) {
	var out CoderDefaultAgentConfig
	v, err := optionalConfigMap(values, "default-agent", path, "modelKey", "reasoningEffort", "budget")
	if err != nil {
		return out, err
	}
	out.ModelKey, err = configText(v, "modelKey", path+".default-agent")
	if err != nil {
		return out, err
	}
	out.ReasoningEffort, err = configText(v, "reasoningEffort", path+".default-agent")
	if err != nil {
		return out, err
	}
	if raw, ok := v["budget"]; ok {
		b, ok := raw.(map[string]any)
		if !ok {
			return out, fmt.Errorf("%s.default-agent.budget must be an object", path)
		}
		out.Budget = cloneConfigMap(b)
	}
	return out, nil
}
func parseWorkspaceAgents(values map[string]any, path string) (CoderWorkspaceAgentsConfig, error) {
	var out CoderWorkspaceAgentsConfig
	if _, exists := values["workspace-agents"]; !exists {
		return out, nil
	}
	v, err := optionalConfigMap(values, "workspace-agents", path, "file")
	if err != nil {
		return out, err
	}
	file, err := configText(v, "file", path+".workspace-agents")
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(file) == "" {
		return out, fmt.Errorf("%s.workspace-agents.file must not be empty", path)
	}
	return CoderWorkspaceAgentsConfig{Enabled: true, File: file}, nil
}
func (c *Config) applyAgentSettingsFile(path string) error {
	values, err := loadYAMLMap(path)
	if err != nil {
		return err
	}
	values, err = configMap(values, path, "preset-tools", "preset-connectors", "general", "coder", "kbase", "acp-bridges")
	if err != nil {
		return err
	}
	presets, err := parseAgentPresets(values, path)
	if err != nil {
		return err
	}
	c.PresetTools, c.PresetConnectors = presets.Tools, presets.Connectors
	c.ModePresets = map[string]AgentPresets{}
	c.ACP.SourcePath = path
	c.ACP.ACPBridges, err = parseCoderACPBridges(values["acp-bridges"], nil)
	if err != nil {
		return err
	}
	for _, mode := range []string{"general", "coder", "kbase"} {
		keys := []string{"preset-tools", "preset-connectors", "default-agent"}
		if mode != "kbase" {
			keys = append(keys, "workspace-agents")
		}
		v, err := optionalConfigMap(values, mode, path, keys...)
		if err != nil {
			return err
		}
		p, err := parseAgentPresets(v, path+"."+mode)
		if err != nil {
			return err
		}
		c.ModePresets[mode] = p
		d, err := parseAgentDefaults(v, path+"."+mode)
		if err != nil {
			return err
		}
		w, err := parseWorkspaceAgents(v, path+"."+mode)
		if err != nil {
			return err
		}
		switch mode {
		case "general":
			c.GeneralSettings = GeneralSettingsConfig{DefaultAgent: d, WorkspaceAgents: w}
		case "coder":
			c.CoderSettings = CoderSettingsConfig{DefaultAgent: d, WorkspaceAgents: w}
		case "kbase":
			if d.Budget != nil {
				return fmt.Errorf("%s.kbase.default-agent.budget is unsupported", path)
			}
			c.KBase.DefaultAgent.ModelKey = d.ModelKey
			c.KBase.DefaultAgent.ReasoningEffort = d.ReasoningEffort
		}
	}
	return nil
}
func (c *Config) applyKBXValues(raw any) error {
	v, err := configMap(raw, "runtime.kbx", "embedding")
	if err != nil {
		return err
	}
	e, err := optionalConfigMap(v, "embedding", "runtime.kbx", "model-key", "prompt")
	if err != nil {
		return err
	}
	key, err := configText(e, "model-key", "runtime.kbx.embedding")
	if err != nil {
		return err
	}
	prompt, err := configText(e, "prompt", "runtime.kbx.embedding")
	if err != nil {
		return err
	}
	if prompt == "" {
		prompt = "raw"
	}
	if prompt != "raw" && prompt != "qwen3" {
		return fmt.Errorf("runtime.kbx.embedding.prompt must be raw or qwen3")
	}
	c.KBX.Embedding.ModelKey = key
	c.KBX.Embedding.Prompt = prompt
	return nil
}
func (c *Config) applyAgentPromptFile(path string) error {
	v, err := loadYAMLMap(path)
	if err != nil {
		return err
	}
	v, err = configMap(v, path, "shared", "coder", "kbase")
	if err != nil {
		return err
	}
	shared, err := optionalConfigMap(v, "shared", path, "runtime", "skill", "tool-appendix", "plan-execute", "btw")
	if err != nil {
		return err
	}
	schemas := map[string][]string{
		"skill":         {"instructions-prompt", "catalog-header", "disclosure-header", "instructions-label"},
		"tool-appendix": {"tool-description-title", "after-call-hint-title"},
		"plan-execute":  {"task-execution-prompt-template", "plan-user-prompt-template", "summary-system-prompt", "summary-user-prompt-template"},
		"btw":           {"user-prompt-template", "final-answer-prompt"},
	}
	for section, keys := range schemas {
		m, err := optionalConfigMap(shared, section, path+".shared", keys...)
		if err != nil {
			return err
		}
		for key := range m {
			if _, err := configText(m, key, path+".shared."+section); err != nil {
				return err
			}
		}
	}
	coder, err := optionalConfigMap(v, "coder", path, "system-prompt", "planning-prompt")
	if err != nil {
		return err
	}
	kbase, err := optionalConfigMap(v, "kbase", path, "system-prompt")
	if err != nil {
		return err
	}
	for section, m := range map[string]map[string]any{"coder": coder, "kbase": kbase} {
		for key := range m {
			if _, err := configText(m, key, path+"."+section); err != nil {
				return err
			}
		}
	}
	if err := c.applyRuntimePrompt(shared, path+".shared"); err != nil {
		return err
	}
	c.applyPromptsValues(shared)
	c.applyCoderPromptsValues(coder)
	c.applyKBasePromptsValues(kbase)
	return nil
}

func validateAIToolValues(values map[string]any) error {
	schemas := map[string][]string{
		"vision-recognize": {"model-key", "timeout", "max-images", "max-image-bytes", "output-format", "system-prompt"},
		"web-fetch":        {"model-key", "timeout", "fetch-timeout", "max-url-length", "max-response-bytes", "max-markdown-chars", "max-output-tokens", "system-prompt"},
		"image-generate":   {"model-key", "timeout", "size", "response-format", "output-mime-type", "max-prompt-chars", "max-images", "max-image-bytes", "persist-artifact"},
	}
	for section, fields := range schemas {
		raw, exists := values[section]
		if !exists {
			continue
		}
		keys := []string{"enabled", "default-profile", "profiles"}
		if section == "web-fetch" {
			keys = append(keys, "preapproved-hosts")
		}
		v, err := configMap(raw, "tools."+section, keys...)
		if err != nil {
			return err
		}
		if raw, ok := v["enabled"]; ok {
			if _, ok := raw.(bool); !ok {
				return fmt.Errorf("tools.%s.enabled must be boolean", section)
			}
		}
		if _, err = configText(v, "default-profile", "tools."+section); err != nil {
			return err
		}
		if raw, ok := v["profiles"]; ok {
			profiles, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("tools.%s.profiles must be an object", section)
			}
			for name, raw := range profiles {
				path := "tools." + section + ".profiles." + name
				if strings.TrimSpace(name) == "" {
					return fmt.Errorf("%s: empty profile name", path)
				}
				p, err := configMap(raw, path, fields...)
				if err != nil {
					return err
				}
				for key, value := range p {
					switch key {
					case "timeout", "fetch-timeout", "max-images", "max-image-bytes", "max-url-length", "max-response-bytes", "max-markdown-chars", "max-output-tokens", "max-prompt-chars":
						if _, ok := value.(int64); !ok {
							return fmt.Errorf("%s.%s must be an integer", path, key)
						}
					case "persist-artifact":
						if _, ok := value.(bool); !ok {
							return fmt.Errorf("%s.%s must be boolean", path, key)
						}
					default:
						if _, err := configText(p, key, path); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}
