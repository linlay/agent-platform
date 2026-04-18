package catalog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

func resolveDirectoryAgentConfig(dirPath string) string {
	for _, candidate := range []string{"agent.yml", "agent.yaml"} {
		path := filepath.Join(dirPath, candidate)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func loadAgents(root, marketDir string) (map[string]AgentDefinition, error) {
	items := map[string]AgentDefinition{}
	err := visitRuntimeEntries(
		root,
		func(root string) {
			log.Printf("[catalog][agents] directory not found: %s", root)
		},
		func(name string, _ os.DirEntry) bool {
			return !strings.HasPrefix(name, ".") && ShouldLoadRuntimeName(name)
		},
		func(name string, entry os.DirEntry) {
			if entry.IsDir() {
				agentDir := filepath.Join(root, name)
				configPath := resolveDirectoryAgentConfig(agentDir)
				if configPath == "" {
					log.Printf("[catalog][agents] skip directory %s: no agent.yml or agent.yaml found", name)
					return
				}
				def, err := parseAgentFileWithPrompts(configPath, agentDir)
				if err != nil {
					log.Printf("[catalog][agents] skip directory %s: parse error: %v", name, err)
					return
				}
				if def.Key != name {
					log.Printf("[catalog][agents] skip directory %s: key mismatch (file key=%q, directory=%q)", name, def.Key, name)
					return
				}
				def.AgentDir = agentDir
				if marketDir != "" && len(def.Skills) > 0 {
					if err := reconcileDeclaredSkills(agentDir, def.Skills, marketDir); err != nil {
						log.Printf("[catalog][skills] sync %s: %v", def.Key, err)
					}
				}
				items[def.Key] = def
				return
			}
			if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
				return
			}
			def, err := parseAgentFile(filepath.Join(root, name))
			if err != nil {
				log.Printf("[catalog][agents] skip file %s: parse error: %v", name, err)
				return
			}
			items[def.Key] = def
		},
	)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func loadAgentPrompts(agentDir string, def *AgentDefinition, root map[string]any) {
	if agentDir == "" {
		return
	}

	def.SoulPrompt = readOptionalMarkdown(filepath.Join(agentDir, "SOUL.md"))
	def.MemoryPrompt = readOptionalMarkdown(filepath.Join(agentDir, "memory", "memory.md"))

	topPromptFiles := parsePromptFileField(root["promptFile"])

	switch def.Mode {
	case "PLAN_EXECUTE":
		pe := mapNode(root["planExecute"])
		def.PlanPrompt = resolveStagePrompt(agentDir, mapNode(pe["plan"]), topPromptFiles, root)
		def.ExecutePrompt = resolveStagePrompt(agentDir, mapNode(pe["execute"]), topPromptFiles, root)
		def.SummaryPrompt = resolveStagePrompt(agentDir, mapNode(pe["summary"]), topPromptFiles, root)
	default:
		if len(topPromptFiles) > 0 {
			def.AgentsPrompt = loadPromptMarkdowns(agentDir, topPromptFiles)
		}
		if def.AgentsPrompt == "" {
			def.AgentsPrompt = readOptionalMarkdown(filepath.Join(agentDir, "AGENTS.md"))
		}
	}
}

func resolveStagePrompt(agentDir string, stageConfig map[string]any, topPromptFiles []string, root map[string]any) string {
	stageFiles := parsePromptFileField(stageConfig["promptFile"])
	if len(stageFiles) > 0 {
		if content := loadPromptMarkdowns(agentDir, stageFiles); content != "" {
			return content
		}
	}
	if len(topPromptFiles) > 0 {
		if content := loadPromptMarkdowns(agentDir, topPromptFiles); content != "" {
			return content
		}
	}
	return readOptionalMarkdown(filepath.Join(agentDir, "AGENTS.md"))
}

func parsePromptFileField(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return []string{strings.TrimSpace(v)}
		}
		return nil
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
		return result
	case []string:
		result := make([]string, 0, len(v))
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
		return result
	default:
		return nil
	}
}

func normalizeContextTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	seen := map[string]struct{}{}
	for _, raw := range tags {
		tag := normalizeContextTag(raw)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func normalizeContextTag(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	switch tag {
	case "system", "context", "owner", "auth", "sandbox", "all-agents", "memory":
		return tag
	case "agent_identity", "run_session", "scene", "references", "execution_policy":
		return "context"
	case "memory_context":
		return "memory"
	case "skills":
		return "context"
	default:
		return tag
	}
}

func parseRuntimePrompts(root map[string]any) AgentRuntimePrompts {
	if len(root) == 0 {
		return AgentRuntimePrompts{}
	}
	skill := mapNode(root["skill"])
	toolAppendix := mapNode(root["toolAppendix"])
	if len(toolAppendix) == 0 {
		toolAppendix = mapNode(root["toolAppendixConfig"])
	}
	planExecute := mapNode(root["planExecute"])
	return AgentRuntimePrompts{
		Skill: SkillPromptConfig{
			CatalogHeader:     stringNode(skill["catalogHeader"]),
			DisclosureHeader:  stringNode(skill["disclosureHeader"]),
			InstructionsLabel: stringNode(skill["instructionsLabel"]),
		},
		ToolAppendix: ToolAppendixPromptConfig{
			ToolDescriptionTitle: stringNode(toolAppendix["toolDescriptionTitle"]),
			AfterCallHintTitle:   stringNode(toolAppendix["afterCallHintTitle"]),
		},
		PlanExecute: PlanExecutePromptConfig{
			TaskExecutionPromptTemplate: stringNode(planExecute["taskExecutionPromptTemplate"]),
		},
	}
}

func loadPromptMarkdowns(agentDir string, promptFiles []string) string {
	var parts []string
	root := filepath.Clean(agentDir)
	for _, file := range promptFiles {
		if filepath.IsAbs(file) {
			log.Printf("[catalog][agents] skip absolute promptFile path: %s", file)
			continue
		}
		resolved := filepath.Clean(filepath.Join(root, file))
		if !strings.HasPrefix(resolved, root) {
			log.Printf("[catalog][agents] skip promptFile escaping agent dir: %s", file)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(file), ".md") {
			log.Printf("[catalog][agents] skip non-.md promptFile: %s", file)
			continue
		}
		content := readOptionalMarkdown(resolved)
		if content != "" {
			parts = append(parts, content)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

func readOptionalMarkdown(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func parseAgentFileWithPrompts(path string, agentDir string) (AgentDefinition, error) {
	def, tree, err := parseAgentFileRaw(path)
	if err != nil {
		return def, err
	}
	loadAgentPrompts(agentDir, &def, tree)
	return def, nil
}

func parseAgentFile(path string) (AgentDefinition, error) {
	def, _, err := parseAgentFileRaw(path)
	return def, err
}

func parseAgentFileRaw(path string) (AgentDefinition, map[string]any, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return AgentDefinition{}, nil, fmt.Errorf("agent file must be a map")
	}
	def := AgentDefinition{
		Key:         stringNode(root["key"]),
		Name:        stringNode(root["name"]),
		Icon:        root["icon"],
		Description: stringNode(root["description"]),
		Role:        stringNode(root["role"]),
		Mode:        strings.ToUpper(defaultString(stringNode(root["mode"]), "ONESHOT")),
	}
	modelConfig := mapNode(root["modelConfig"])
	def.ModelKey = stringNode(modelConfig["modelKey"])
	toolConfig := mapNode(root["toolConfig"])
	def.Tools = listStrings(toolConfig["tools"])
	def.ToolOverrides = parseToolOverrides(toolConfig["overrides"])
	def.Skills = listStrings(mapNode(root["skillConfig"])["skills"])
	def.Controls = cloneListMaps(listMaps(root["controls"]))
	contextConfig := mapNode(root["contextConfig"])
	contextTags := listStrings(contextConfig["tags"])
	if len(contextTags) == 0 {
		contextTags = listStrings(root["contextTags"])
	}
	def.ContextTags = normalizeContextTags(contextTags)
	if budget := mapNode(root["budget"]); len(budget) > 0 {
		def.Budget = contracts.CloneMap(budget)
	}
	if stageSettings := mapNode(root["stageSettings"]); len(stageSettings) > 0 {
		def.StageSettings = contracts.CloneMap(stageSettings)
	}
	def.StageSettings = applyModelReasoningDefaults(def.StageSettings, mapNode(modelConfig["reasoning"]))
	if proxyRaw := mapNode(root["proxyConfig"]); len(proxyRaw) > 0 {
		def.ProxyConfig = &ProxyConfig{
			BaseURL:   stringNode(proxyRaw["baseUrl"]),
			Token:     stringNode(proxyRaw["token"]),
			TimeoutMs: intNode(proxyRaw["timeoutMs"]),
		}
		if def.ProxyConfig.TimeoutMs <= 0 {
			def.ProxyConfig.TimeoutMs = 300000
		}
	}
	def.RuntimePrompts = parseRuntimePrompts(mapNode(root["runtimePrompts"]))
	if strings.TrimSpace(def.RuntimePrompts.PlanExecute.TaskExecutionPromptTemplate) != "" {
		if def.StageSettings == nil {
			def.StageSettings = map[string]any{}
		}
		if strings.TrimSpace(stringNode(def.StageSettings["taskExecutionPromptTemplate"])) == "" {
			def.StageSettings["taskExecutionPromptTemplate"] = def.RuntimePrompts.PlanExecute.TaskExecutionPromptTemplate
		}
	}
	sandboxConfig := mapNode(root["sandboxConfig"])
	if len(sandboxConfig) > 0 {
		def.Sandbox = map[string]any{
			"environmentId": stringNode(sandboxConfig["environmentId"]),
			"level":         strings.ToLower(stringNode(sandboxConfig["level"])),
		}
		if mounts := listMaps(sandboxConfig["extraMounts"]); len(mounts) > 0 {
			def.Sandbox["extraMounts"] = cloneListMaps(mounts)
		}
	}
	def.ReactMaxSteps = intNode(mapNode(root["react"])["maxSteps"])

	if len(def.Skills) > 0 && !containsString(def.Tools, "_sandbox_bash_") {
		def.Tools = append(def.Tools, "_sandbox_bash_")
	}
	memoryConfig := mapNode(root["memoryConfig"])
	if enabled, ok := memoryConfig["enabled"].(bool); ok && enabled {
		for _, memTool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
			if !containsString(def.Tools, memTool) {
				def.Tools = append(def.Tools, memTool)
			}
		}
	}

	if def.Key == "" {
		return AgentDefinition{}, nil, fmt.Errorf("agent key is required")
	}
	if def.Name == "" {
		def.Name = def.Key
	}
	if def.Description == "" {
		def.Description = def.Key
	}
	if def.Role == "" {
		def.Role = def.Name
	}
	return def, root, nil
}

func applyModelReasoningDefaults(stageSettings map[string]any, reasoning map[string]any) map[string]any {
	if len(reasoning) == 0 {
		return stageSettings
	}
	enabled, enabledOK := reasoning["enabled"]
	effort, effortOK := reasoning["effort"]
	if !enabledOK && !effortOK {
		return stageSettings
	}
	if stageSettings == nil {
		stageSettings = map[string]any{}
	}
	applyReasoningDefaultsToStageNode(stageSettings, enabled, enabledOK, effort, effortOK)
	for _, key := range []string{"plan", "execute", "summary"} {
		node := mapNode(stageSettings[key])
		if len(node) == 0 {
			continue
		}
		applyReasoningDefaultsToStageNode(node, enabled, enabledOK, effort, effortOK)
		stageSettings[key] = node
	}
	return stageSettings
}

func applyReasoningDefaultsToStageNode(node map[string]any, enabled any, enabledOK bool, effort any, effortOK bool) {
	if enabledOK {
		if _, exists := node["reasoningEnabled"]; !exists {
			node["reasoningEnabled"] = enabled
		}
	}
	if effortOK {
		if _, exists := node["reasoningEffort"]; !exists {
			node["reasoningEffort"] = effort
		}
	}
}
