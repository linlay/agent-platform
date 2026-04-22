package catalog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"agent-platform-runner-go/internal/api"
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
	warnLegacySoulPrompt(agentDir, def.Key, def.SoulPrompt)
	def.StaticMemoryPrompt = readOptionalMarkdown(filepath.Join(agentDir, "memory", "memory.md"))
	def.MemoryPrompt = def.StaticMemoryPrompt

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

func warnLegacySoulPrompt(agentDir string, agentKey string, soulPrompt string) {
	if strings.TrimSpace(soulPrompt) == "" {
		return
	}
	var legacy []string
	if hasMarkdownHeading(soulPrompt, "# Identity") {
		legacy = append(legacy, "# Identity")
	}
	if hasMarkdownHeading(soulPrompt, "## Mission") {
		legacy = append(legacy, "## Mission")
	}
	if len(legacy) == 0 {
		return
	}
	log.Printf("[catalog][agents] legacy SOUL.md headings in %s (%s): %s; move identity fields to agent.yml and rewrite SOUL.md as behavior-only prompt", agentKey, filepath.Join(agentDir, "SOUL.md"), strings.Join(legacy, ", "))
}

func hasMarkdownHeading(content string, heading string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == heading {
			return true
		}
	}
	return false
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
	case "system", "context", "owner", "auth", "all-agents", "memory":
		return tag
	case "sandbox":
		return ""
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
		Wonders:     normalizeWonderStrings(root["wonders"]),
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
		sandboxEnv, err := parseSandboxEnv(sandboxConfig["env"])
		if err != nil {
			return AgentDefinition{}, nil, err
		}
		if len(sandboxEnv) > 0 {
			def.Sandbox["env"] = sandboxEnv
		}
		if mounts := listMaps(sandboxConfig["extraMounts"]); len(mounts) > 0 {
			def.Sandbox["extraMounts"] = cloneListMaps(mounts)
		}
	}
	def.ReactMaxSteps = intNode(mapNode(root["react"])["maxSteps"])

	if err := validateReservedBashToolNames(def.Tools, def.ToolOverrides); err != nil {
		return AgentDefinition{}, nil, err
	}
	if (len(def.Skills) > 0 || len(def.Sandbox) > 0) && !containsString(def.Tools, "_bash_") {
		def.Tools = append(def.Tools, "_bash_")
	}
	memoryConfig := mapNode(root["memoryConfig"])
	memoryToolsEnabled := true
	if enabled, ok := memoryConfig["enabled"].(bool); ok {
		memoryToolsEnabled = enabled
	}
	if memoryToolsEnabled {
		for _, memTool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
			if !containsString(def.Tools, memTool) {
				def.Tools = append(def.Tools, memTool)
			}
		}
		if managementTools, ok := memoryConfig["managementTools"].(bool); ok && managementTools {
			for _, memTool := range []string{"_memory_update_", "_memory_forget_", "_memory_timeline_", "_memory_promote_", "_memory_consolidate_"} {
				if !containsString(def.Tools, memTool) {
					def.Tools = append(def.Tools, memTool)
				}
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

func validateReservedBashToolNames(tools []string, overrides map[string]api.ToolDetailResponse) error {
	for _, tool := range tools {
		if err := validateReservedBashToolName(tool, "toolConfig.tools"); err != nil {
			return err
		}
	}
	for rawName, override := range overrides {
		if err := validateReservedBashToolName(rawName, "toolConfig.overrides"); err != nil {
			return err
		}
		if err := validateReservedBashToolName(override.Name, "toolConfig.overrides.*.name"); err != nil {
			return err
		}
		if err := validateReservedBashToolName(override.Key, "toolConfig.overrides.*.key"); err != nil {
			return err
		}
	}
	return nil
}

func validateReservedBashToolName(value string, field string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "_sandbox_bash_", "_bash_container_":
		return fmt.Errorf("%s must use _bash_ instead of %s", field, strings.TrimSpace(value))
	default:
		return nil
	}
}

func normalizeWonderStrings(value any) []string {
	raw := listStrings(value)
	if len(raw) == 0 {
		return nil
	}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		items = append(items, trimmed)
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func parseSandboxEnv(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("sandboxConfig.env must be a map[string]string")
	}
	if len(root) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(root))
	for key, rawValue := range root {
		if err := validateSandboxEnvKey(key); err != nil {
			return nil, err
		}
		stringValue, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("sandboxConfig.env[%q] must be a string", key)
		}
		result[key] = stringValue
	}
	return result, nil
}

func validateSandboxEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("sandboxConfig.env contains an empty key")
	}
	if strings.ContainsRune(key, '=') {
		return fmt.Errorf("sandboxConfig.env key %q must not contain '='", key)
	}
	for _, r := range key {
		if unicode.IsSpace(r) {
			return fmt.Errorf("sandboxConfig.env key %q must not contain whitespace", key)
		}
	}
	return nil
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
