package catalog

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	agentbuiltin "agent-platform/internal/agent/builtin"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/agentconfig"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
	"agent-platform/internal/rootpaths"

	"agent-platform/internal/interaction"
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

func loadAgentsWithAdminAssembler(root, chatsDir string, globalMemoryEnabled bool, assembler *runtimeAgentAssembler) (map[string]AgentDefinition, map[string]AdminAgent, error) {
	assembler.refreshedAgents = map[string]bool{}
	items := map[string]AgentDefinition{}
	adminItems := map[string]AdminAgent{}
	expectedRuntimeAgents := map[string]struct{}{}
	for key, def := range assembler.frozenAgents {
		items[key] = def
		adminItems[key] = assembler.frozenAdmin[key]
		expectedRuntimeAgents[key] = struct{}{}
	}
	err := visitRuntimeEntries(
		root,
		func(root string) {
			log.Printf("[catalog][agents] directory not found: %s", root)
		},
		func(name string, _ os.DirEntry) bool {
			return !strings.HasPrefix(name, ".") && ShouldLoadRuntimeName(name)
		},
		func(name string, entry os.DirEntry) {
			if source, ok := runtimeAgentSource(root, name, entry); ok {
				key := adminAgentFallbackKey(source)
				if definition, err := readAdminAgentDefinitionMap(source.Path); err == nil {
					key = adminAgentKey(source, key, definition)
				}
				if validRuntimeComponent(key) {
					expectedRuntimeAgents[key] = struct{}{}
				}
			}
			// Individual Agent definitions are isolated: loadAgentSourceIntoMaps
			// records diagnostics and preserves an invalid AdminAgent entry when
			// parsing or validation fails, while valid Agents remain available.
			// Root traversal failures are still returned by visitRuntimeEntries.
			_ = loadAgentSourceIntoMaps(root, name, entry, chatsDir, globalMemoryEnabled, assembler, items, adminItems)
		},
	)
	if err != nil {
		return nil, nil, err
	}
	if err := assembler.cleanupDeleted(expectedRuntimeAgents); err != nil {
		log.Printf("[catalog][agents] cleanup deleted runtime agents: %v", err)
	}
	return items, adminItems, nil
}

func loadAgentSourceIntoMaps(root string, name string, entry os.DirEntry, chatsDir string, globalMemoryEnabled bool, assembler *runtimeAgentAssembler, items map[string]AgentDefinition, adminItems map[string]AdminAgent) error {
	source, ok := runtimeAgentSource(root, name, entry)
	if !ok {
		return nil
	}
	fallbackKey := adminAgentFallbackKey(source)
	definition, err := readAdminAgentDefinitionMap(source.Path)
	if err != nil {
		log.Printf("[catalog][agents] skip %s %s: parse error: %v", source.Kind, name, err)
		adminItems[fallbackKey] = invalidAdminAgent(source, fallbackKey, nil, "invalid_yaml", err)
		return err
	}
	adminKey := adminAgentKey(source, fallbackKey, definition)
	def, _, err := parseAgentFileRaw(source.Path)
	if err != nil {
		log.Printf("[catalog][agents] skip %s %s: parse error: %v", source.Kind, name, err)
		adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_config", err)
		return err
	}
	if source.Kind == "directory" && def.Key != name {
		err := fmt.Errorf("key mismatch (file key=%q, directory=%q)", def.Key, name)
		log.Printf("[catalog][agents] skip directory %s: %v", name, err)
		adminItems[fallbackKey] = invalidAdminAgent(source, fallbackKey, definition, "key_mismatch", err)
		return err
	}

	if def.KBaseConfig.Enabled {
		if err := knowledge.ValidateWorkspaceChatsSeparation(def.Workspace.Root, chatsDir); err != nil {
			log.Printf("[catalog][agents] skip %s %s: KBASE workspace/chats overlap: %v", source.Kind, name, err)
			adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_kbase_workspace_overlap", err)
			return err
		}
	}
	if strings.TrimSpace(def.Workspace.Root) != "" {
		if err := validateAgentWorkspace(def.Workspace); err != nil {
			log.Printf("[catalog][agents] skip %s %s: invalid workspace: %v", source.Kind, name, err)
			adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_workspace", err)
			return err
		}
		if _, err := rootpaths.New(def.Workspace.Root, chatsDir, ""); err != nil {
			log.Printf("[catalog][agents] skip %s %s: invalid workspace/chats relation: %v", source.Kind, name, err)
			adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_workspace_overlap", err)
			return err
		}
	}
	if err := assembler.resolveConnectors(&def); err != nil {
		adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_connector", err)
		return err
	}
	if strings.EqualFold(def.Mode, AgentModeKBase) && !kbaseAgentHasFileTool(def.Tools) {
		log.Printf("[catalog][agents] warning code=kbase_file_tools_missing agent=%q message=KBASE effective tools contain none of %v; check preset-tools, toolConfig.tools and excludeTools", def.Key, agentkbase.StructuredFileToolNames())
	}
	runtimeDir, err := assembler.assemble(source, def)
	if errors.Is(err, errAgentRuntimeBusy) {
		return nil
	}
	if err != nil {
		code := runtimeAgentAssemblyDiagnosticCode(err)
		log.Printf("[catalog][agents] skip %s %s: runtime assembly failed: %v", source.Kind, name, err)
		adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, code, err)
		return err
	}
	def.AgentDir = source.AgentDir
	def.RuntimeDir = runtimeDir
	if err := def.bindConnectorRuntime(); err != nil {
		adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_connector", err)
		return err
	}
	loadAgentPrompts(runtimeDir, &def, definition)
	def = applyGlobalAgentFlags(def, globalMemoryEnabled)
	items[def.Key] = def
	adminItems[def.Key] = readyAdminAgent(def, source, definition)
	assembler.refreshedAgents[def.Key] = true
	return nil
}

func runtimeAgentSource(root string, name string, entry os.DirEntry) (EditableAgentSource, bool) {
	if entry.IsDir() {
		agentDir := filepath.Join(root, name)
		configPath := resolveDirectoryAgentConfig(agentDir)
		if configPath == "" {
			log.Printf("[catalog][agents] skip directory %s: no agent.yml or agent.yaml found", name)
			return EditableAgentSource{}, false
		}
		return EditableAgentSource{Kind: "directory", Path: configPath, AgentDir: agentDir}, true
	}
	lowerName := strings.ToLower(name)
	if !strings.HasSuffix(lowerName, ".yml") && !strings.HasSuffix(lowerName, ".yaml") {
		return EditableAgentSource{}, false
	}
	return EditableAgentSource{Kind: "file", Path: filepath.Join(root, name)}, true
}

func readAdminAgentDefinitionMap(path string) (map[string]any, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return nil, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agent file must be a map")
	}
	return root, nil
}

func adminAgentFallbackKey(source EditableAgentSource) string {
	if source.Kind == "directory" && strings.TrimSpace(source.AgentDir) != "" {
		return filepath.Base(filepath.Clean(source.AgentDir))
	}
	return LogicalRuntimeBaseName(filepath.Base(source.Path))
}

func adminAgentKey(source EditableAgentSource, fallbackKey string, definition map[string]any) string {
	if source.Kind == "directory" {
		return fallbackKey
	}
	if key := stringNode(definition["key"]); key != "" {
		return key
	}
	return fallbackKey
}

func invalidAdminAgent(source EditableAgentSource, key string, definition map[string]any, code string, err error) AdminAgent {
	item := adminAgentFromDefinition(source, key, definition)
	item.Status = AdminAgentStatusInvalid
	item.Diagnostics = []AdminAgentDiagnostic{{
		Severity:   "error",
		Code:       code,
		Message:    err.Error(),
		SourcePath: source.Path,
	}}
	return item
}

func readyAdminAgent(def AgentDefinition, source EditableAgentSource, definition map[string]any) AdminAgent {
	apiMode := AgentModeForAPI(def.Mode)
	item := AdminAgent{
		Key:           def.Key,
		Name:          firstNonBlankString(def.Name, def.Key),
		Icon:          def.Icon,
		Description:   def.Description,
		Role:          def.Role,
		Mode:          apiMode,
		ModelKey:      def.ModelKey,
		Tools:         append([]string(nil), def.Tools...),
		Skills:        append([]string(nil), def.Skills...),
		Workspace:     def.Workspace,
		Controls:      cloneListMaps(def.Controls),
		ChannelConfig: cloneAgentChannelConfig(def.ChannelConfig),
		Status:        AdminAgentStatusReady,
		Source:        source,
		Definition:    contracts.CloneMap(definition),
		SoulPrompt:    def.SoulPrompt,
		AgentsPrompt:  def.AgentsPrompt,
	}
	item.Meta = adminAgentMeta(item, EffectiveAgentVisibilityScopes(def))
	return item
}

func adminAgentFromDefinition(source EditableAgentSource, key string, definition map[string]any) AdminAgent {
	if key == "" {
		key = adminAgentFallbackKey(source)
	}
	modelConfig := mapNode(definition["modelConfig"])
	toolConfig := mapNode(definition["toolConfig"])
	runtimeConfig := mapNode(definition["runtimeConfig"])
	mode := ""
	if rawMode := stringNode(definition["mode"]); rawMode != "" {
		mode = AgentModeForAPI(rawMode)
	} else if strings.EqualFold(stringNode(definition["engine"]), AgentEngineACP) {
		mode = AgentModeCoder
	}
	soulPrompt := ""
	agentsPrompt := ""
	if source.Kind == "directory" && strings.TrimSpace(source.AgentDir) != "" {
		soulPrompt = readOptionalMarkdown(filepath.Join(source.AgentDir, "SOUL.md"))
		agentsPrompt = readOptionalMarkdown(filepath.Join(source.AgentDir, "AGENTS.md"))
	}
	item := AdminAgent{
		Key:           key,
		Name:          firstNonBlankString(stringNode(definition["name"]), key),
		Icon:          definition["icon"],
		Description:   stringNode(definition["description"]),
		Role:          stringNode(definition["role"]),
		Mode:          mode,
		ModelKey:      stringNode(modelConfig["modelKey"]),
		Tools:         listStrings(toolConfig["tools"]),
		Skills:        listStrings(mapNode(definition["skillConfig"])["skills"]),
		Workspace:     parseAgentWorkspaceRoot(runtimeConfig["workspaceRoot"]),
		Controls:      cloneListMaps(listMaps(definition["controls"])),
		ChannelConfig: parseAgentChannelConfig(definition["channelConfig"]),
		Source:        source,
		Definition:    contracts.CloneMap(definition),
		SoulPrompt:    soulPrompt,
		AgentsPrompt:  agentsPrompt,
	}
	item.Meta = adminAgentMeta(item, parseAgentVisibilityScopes(definition["visibility"]))
	return item
}

func adminAgentMeta(item AdminAgent, visibilityScopes []string) map[string]any {
	meta := map[string]any{
		"model":       item.ModelKey,
		"modelKey":    item.ModelKey,
		"mode":        item.Mode,
		"tools":       append([]string(nil), item.Tools...),
		"toolsCount":  len(item.Tools),
		"skills":      append([]string(nil), item.Skills...),
		"skillsCount": len(item.Skills),
		"visibility": map[string]any{
			"scopes": append([]string(nil), visibilityScopes...),
		},
	}
	if channelMeta := agentChannelConfigMeta(item.ChannelConfig, item.Key); len(channelMeta) > 0 {
		meta["channelConfig"] = channelMeta
	}
	return meta
}

func cloneAgentChannelConfig(src AgentChannelConfig) AgentChannelConfig {
	dst := src
	dst.Exports = append([]AgentChannelExport(nil), src.Exports...)
	return dst
}

func agentChannelConfigMeta(cfg AgentChannelConfig, localAgentKey string) map[string]any {
	meta := map[string]any{}
	if strings.TrimSpace(cfg.ChannelID) != "" {
		meta["channelId"] = strings.TrimSpace(cfg.ChannelID)
	}
	if strings.TrimSpace(cfg.RemoteAgentKey) != "" {
		meta["remoteAgentKey"] = strings.TrimSpace(cfg.RemoteAgentKey)
	}
	if len(cfg.Exports) > 0 {
		exports := make([]map[string]any, 0, len(cfg.Exports))
		for _, export := range cfg.Exports {
			exports = append(exports, map[string]any{
				"channelId":        strings.TrimSpace(export.ChannelID),
				"externalAgentKey": EffectiveChannelExportExternalKey(localAgentKey, export),
				"allow": map[string]any{
					"query":        export.Allow.Query,
					"submit":       export.Allow.Submit,
					"steer":        export.Allow.Steer,
					"interrupt":    export.Allow.Interrupt,
					"fileTransfer": export.Allow.FileTransfer,
				},
			})
		}
		meta["exports"] = exports
	}
	return meta
}

func loadAgentPrompts(agentDir string, def *AgentDefinition, root map[string]any) {
	if agentDir == "" {
		return
	}

	def.SoulPrompt = readOptionalMarkdown(filepath.Join(agentDir, "SOUL.md"))

	topPromptFiles := parsePromptFileField(root["promptFile"])

	switch def.Mode {
	case "PLAN_EXECUTE":
		stageSettings := mapNode(root["stageSettings"])
		def.PlanPrompt = resolveStagePrompt(agentDir, "plan", mapNode(stageSettings["plan"]), topPromptFiles)
		def.ExecutePrompt = resolveStagePrompt(agentDir, "execute", mapNode(stageSettings["execute"]), topPromptFiles)
		def.SummaryPrompt = resolveStagePrompt(agentDir, "summary", mapNode(stageSettings["summary"]), topPromptFiles)
	default:
		if len(topPromptFiles) > 0 {
			def.AgentsPrompt = loadPromptMarkdowns(agentDir, topPromptFiles)
		}
		if def.AgentsPrompt == "" {
			def.AgentsPrompt = readOptionalMarkdown(filepath.Join(agentDir, "AGENTS.md"))
		}
	}
}

func resolveStagePrompt(agentDir string, stage string, stageConfig map[string]any, topPromptFiles []string) string {
	stageFiles := parsePromptFileField(stageConfig["promptFile"])
	if len(stageFiles) > 0 {
		if content := loadPromptMarkdowns(agentDir, stageFiles); content != "" {
			return content
		}
	}
	if content := readOptionalMarkdown(filepath.Join(agentDir, "AGENTS."+stage+".md")); content != "" {
		return content
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
	case "system", "session", "owner", "agents", "memory-global", "memory-agent":
		return tag
	default:
		return ""
	}
}

func parseContextAgents(value any) ([]string, error) {
	var raw []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string, []any, []string:
		raw = listStrings(typed)
	default:
		return nil, fmt.Errorf("contextConfig.agents must be \"*\", a comma-separated string, or a list of agent keys")
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			agentKey := strings.TrimSpace(part)
			if agentKey == "" {
				continue
			}
			if agentKey == "*" {
				return nil, nil
			}
			if _, ok := seen[agentKey]; ok {
				continue
			}
			seen[agentKey] = struct{}{}
			out = append(out, agentKey)
		}
	}
	return out, nil
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
	}
}

func validateAgentToolConfig(toolConfig map[string]any) error {
	for _, key := range []string{"backends", "frontends", "actions", "overrides"} {
		if _, exists := toolConfig[key]; exists {
			return fmt.Errorf("toolConfig.%s is no longer supported; use toolConfig.tools", key)
		}
	}
	if _, exists := toolConfig["mcp-servers"]; exists {
		return fmt.Errorf("toolConfig.mcp-servers was removed; migrate to connectorConfig.connectors")
	}
	return nil
}

func mergeStageSettingsBudgets(budget map[string]any, stageSettings map[string]any) map[string]any {
	stageBudgets := stageBudgetsFromStageSettings(stageSettings)
	if len(stageBudgets) == 0 {
		return budget
	}
	merged := contracts.CloneMap(budget)
	if merged == nil {
		merged = map[string]any{}
	}
	stages := contracts.CloneMap(mapNode(merged["stages"]))
	if stages == nil {
		stages = map[string]any{}
	}
	for stage, stageBudget := range stageBudgets {
		stages[stage] = mergeStageBudgetNodes(stages[stage], stageBudget)
	}
	merged["stages"] = stages
	return merged
}

func stageBudgetsFromStageSettings(stageSettings map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, stage := range []string{"plan", "planning", "execute", "summary"} {
		node := mapNode(stageSettings[stage])
		if len(node) == 0 {
			continue
		}
		stageBudget := allowedStageBudgetNode(mapNode(node["budget"]))
		if len(stageBudget) > 0 {
			out[stage] = stageBudget
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeStageBudgetNodes(base any, override map[string]any) map[string]any {
	merged := contracts.CloneMap(mapNode(base))
	if merged == nil {
		merged = map[string]any{}
	}
	if value, exists := override["maxSteps"]; exists {
		merged["maxSteps"] = value
	}
	if overrideTool := mapNode(override["tool"]); len(overrideTool) > 0 {
		tool := contracts.CloneMap(mapNode(merged["tool"]))
		if tool == nil {
			tool = map[string]any{}
		}
		for key, value := range overrideTool {
			tool[key] = value
		}
		merged["tool"] = tool
	}
	return merged
}

func allowedStageBudgetNode(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if value, exists := raw["maxSteps"]; exists {
		out["maxSteps"] = value
	}
	if tool := allowedStageBudgetToolNode(mapNode(raw["tool"])); len(tool) > 0 {
		out["tool"] = tool
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func allowedStageBudgetToolNode(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"timeout", "maxCalls", "retryCount"} {
		if value, exists := raw[key]; exists {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

func parseAgentFileRaw(path string) (AgentDefinition, map[string]any, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	return parseAgentTree(path, tree)
}

func parseAgentTree(path string, tree any) (AgentDefinition, map[string]any, error) {
	root, ok := tree.(map[string]any)
	if !ok {
		return AgentDefinition{}, nil, fmt.Errorf("agent file must be a map")
	}
	def := AgentDefinition{
		Key:              stringNode(root["key"]),
		Name:             stringNode(root["name"]),
		Icon:             root["icon"],
		Description:      stringNode(root["description"]),
		Role:             stringNode(root["role"]),
		Greetings:        normalizeAgentTextList(root["greetings"]),
		Introductions:    normalizeAgentTextList(root["introductions"]),
		Wonders:          normalizeWonderStrings(root["wonders"]),
		VisibilityScopes: parseAgentVisibilityScopes(root["visibility"]),
	}
	mode, engine, err := ParseAgentModeAndEngine(stringNode(root["mode"]), stringNode(root["engine"]))
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	def.Mode = mode
	def.Engine = engine
	interactionConfig, err := interaction.Parse(mode, root["interactionConfig"])
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	def.InteractionConfig = &interactionConfig
	if err := validatePlanningConfig(def.Mode, root); err != nil {
		return AgentDefinition{}, nil, err
	}
	modelConfig := mapNode(root["modelConfig"])
	if err := NormalizeAgentReasoningConfig(path, root); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := validateAgentSamplingConfig(path, root); err != nil {
		return AgentDefinition{}, nil, err
	}
	def.ModelKey = stringNode(modelConfig["modelKey"])
	reasoning := mapNode(modelConfig["reasoning"])
	def.ModelReasoningEffort = stringNode(reasoning["effort"])
	if enabled, ok := reasoning["enabled"].(bool); ok && !enabled {
		def.ModelReasoningEffort = models.ReasoningEffortNone
	}
	def.ServiceTier = stringNode(modelConfig["serviceTier"])
	toolConfig := mapNode(root["toolConfig"])
	if err := validateAgentToolConfig(toolConfig); err != nil {
		return AgentDefinition{}, nil, err
	}
	def.Tools = listStrings(toolConfig["tools"])
	def.DeclaredTools = append([]string{}, def.Tools...)
	if raw, exists := toolConfig["excludeTools"]; exists {
		def.ExcludedTools, err = config.ParseToolNames(raw, "toolConfig.excludeTools")
		if err != nil {
			return AgentDefinition{}, nil, err
		}
	}
	if raw, exists := root["connectorConfig"]; exists {
		config, ok := raw.(map[string]any)
		if !ok {
			return AgentDefinition{}, nil, fmt.Errorf("connectorConfig must be an object")
		}
		for key := range config {
			if key != "connectors" {
				return AgentDefinition{}, nil, fmt.Errorf("unknown connectorConfig field %q", key)
			}
		}
	}
	def.Connectors, err = parseConnectorIDs(mapNode(root["connectorConfig"])["connectors"])
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	def.Skills = listStrings(mapNode(root["skillConfig"])["skills"])
	def.Controls = cloneListMaps(listMaps(root["controls"]))
	contextConfig := mapNode(root["contextConfig"])
	contextTags := listStrings(contextConfig["tags"])
	def.ContextTags = normalizeContextTags(contextTags)
	contextAgents, err := parseContextAgents(contextConfig["agents"])
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	def.ContextAgents = contextAgents
	if budget := mapNode(root["budget"]); len(budget) > 0 {
		def.Budget = contracts.CloneMap(budget)
		delete(def.Budget, "stages")
	}
	if stageSettings := mapNode(root["stageSettings"]); len(stageSettings) > 0 {
		def.StageSettings = contracts.CloneMap(stageSettings)
	}
	def.Budget = mergeStageSettingsBudgets(def.Budget, def.StageSettings)
	def.StageSettings = applyModelReasoningDefaults(def.StageSettings, mapNode(modelConfig["reasoning"]))
	def.StageSettings = applyModelSamplingDefaults(def.StageSettings, mapNode(modelConfig["sampling"]))
	if proxyRaw := mapNode(root["proxyConfig"]); len(proxyRaw) > 0 {
		def.ProxyConfig = &ProxyConfig{
			BaseURL:      stringNode(proxyRaw["baseUrl"]),
			WebSocketURL: stringNode(firstAnyValue(proxyRaw, "webSocketUrl", "websocketUrl", "wsUrl", "ws-url")),
			Transport:    normalizeProxyTransport(stringNode(proxyRaw["transport"])),
			Protocol:     strings.ToLower(stringNode(proxyRaw["protocol"])),
			AgentKey:     stringNode(proxyRaw["agentKey"]),
			ChatID:       stringNode(proxyRaw["chatId"]),
			Token:        resolveProxyToken(proxyRaw),
			TokenEnv:     stringNode(proxyRaw["tokenEnv"]),
			Timeout:      intNode(proxyRaw["timeout"]),
		}
		if def.ProxyConfig.Timeout <= 0 {
			def.ProxyConfig.Timeout = 300
		}
	}
	def.ChannelConfig = parseAgentChannelConfig(root["channelConfig"])
	def.RuntimePrompts = parseRuntimePrompts(mapNode(root["runtimePrompts"]))
	runtimeConfig := mapNode(root["runtimeConfig"])
	if len(runtimeConfig) > 0 {
		if _, exists := runtimeConfig["acpProxyId"]; exists {
			return AgentDefinition{}, nil, fmt.Errorf("runtimeConfig.acpProxyId was removed; use runtimeConfig.acpBridgeId")
		}
		def.ACPBridgeID = stringNode(runtimeConfig["acpBridgeId"])
		def.Runtime = map[string]any{
			"environmentId": stringNode(runtimeConfig["environmentId"]),
			"level":         strings.ToLower(stringNode(runtimeConfig["level"])),
		}
		def.Workspace = parseAgentWorkspaceRoot(runtimeConfig["workspaceRoot"])
		runtimeEnv, err := parseRuntimeEnv(runtimeConfig["env"])
		if err != nil {
			return AgentDefinition{}, nil, err
		}
		if len(runtimeEnv) > 0 {
			def.Runtime["env"] = runtimeEnv
		}
		def.HostAccess, err = parseAgentHostAccess(runtimeConfig["hostAccess"])
		if err != nil {
			return AgentDefinition{}, nil, err
		}
		mounts := listMaps(runtimeConfig["sandboxMounts"])
		if len(mounts) > 0 {
			def.Runtime["sandboxMounts"] = cloneListMaps(mounts)
		}
	}
	def.Project = parseAgentProjectConfig(root["projectConfig"])
	kbaseConfig := mapNode(root["kbaseConfig"])
	// Embedding is deployment-owned; ignore retired Agent overrides.
	delete(kbaseConfig, "embedding")
	def.KBaseConfig, err = knowledge.ParseConfig(kbaseConfig)
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := configureAgentKBaseCapability(&def, kbaseConfig); err != nil {
		return AgentDefinition{}, nil, err
	}
	hasRuntimeSandbox := strings.TrimSpace(stringNode(def.Runtime["environmentId"])) != ""
	if err := validateAgentModeWorkspace(def.Mode, def.Workspace, def.KBaseConfig, hasRuntimeSandbox); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := validateAgentWorkspace(def.Workspace); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := ValidateAgentCoderBackend(def); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := ValidateAgentChannelConfig(def); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := ValidateAgentModelConfig(def); err != nil {
		return AgentDefinition{}, nil, err
	}
	if def.KBaseConfig.Enabled {
		if err := knowledge.ValidateConfig(def.KBaseConfig); err != nil {
			return AgentDefinition{}, nil, err
		}
	}
	def = applyAgentModeProfileDefaults(def)
	if err := ValidateOrdinaryAgentTools(def.Tools); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := validateReservedBashToolNames(def.Tools); err != nil {
		return AgentDefinition{}, nil, err
	}

	def.MemoryConfig, err = parseAgentMemoryConfig(path, root["memoryConfig"])
	if err != nil {
		return AgentDefinition{}, nil, err
	}
	def.MemoryEnabled = def.MemoryConfig.Enabled

	if def.Key == "" {
		return AgentDefinition{}, nil, fmt.Errorf("agent key is required")
	}
	if def.Description == "" {
		def.Description = def.Key
	}
	return def, root, nil
}

func resolveProxyToken(proxyRaw map[string]any) string {
	if len(proxyRaw) == 0 {
		return ""
	}
	if token := strings.TrimSpace(stringNode(proxyRaw["token"])); token != "" {
		return token
	}
	envName := strings.TrimSpace(stringNode(proxyRaw["tokenEnv"]))
	if envName == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func parseAgentHostAccess(value any) (AgentHostAccessConfig, error) {
	node := mapNode(value)
	if len(node) == 0 {
		return AgentHostAccessConfig{}, nil
	}
	readRoots, err := parseAgentHostAccessRoots(node["readRoots"])
	if err != nil {
		return AgentHostAccessConfig{}, fmt.Errorf("runtimeConfig.hostAccess.readRoots: %w", err)
	}
	writeRoots, err := parseAgentHostAccessRoots(node["writeRoots"])
	if err != nil {
		return AgentHostAccessConfig{}, fmt.Errorf("runtimeConfig.hostAccess.writeRoots: %w", err)
	}
	return AgentHostAccessConfig{
		ReadRoots:  readRoots,
		WriteRoots: writeRoots,
	}, nil
}

func parseAgentHostAccessRoots(value any) ([]string, error) {
	roots := listStrings(value)
	out := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, root := range roots {
		cleaned, err := cleanAgentHostAccessRoot(root)
		if err != nil {
			return nil, err
		}
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out, nil
}

func cleanAgentHostAccessRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil
	}
	switch strings.ToLower(root) {
	case "@workspace", "@chat", "@agent", "@skills", "@skills-center", "@owner", "@temp":
		return strings.ToLower(root), nil
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		return filepath.Clean(expandHomeWorkspaceRoot(root)), nil
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root), nil
	}
	return "", fmt.Errorf("%q must be an absolute path, ~/ path, or a supported alias", root)
}

func parseAgentMemoryConfig(path string, value any) (AgentMemoryConfig, error) {
	node := mapNode(value)
	cfg := AgentMemoryConfig{}
	if enabled, ok := node["enabled"].(bool); ok {
		cfg.Enabled = enabled
	}
	for _, key := range []string{"embedding", "autoRemember", "managementTools"} {
		if _, exists := node[key]; exists {
			return cfg, fmt.Errorf("%s: memoryConfig.%s is no longer supported; remove this field", path, key)
		}
	}
	return cfg, nil
}

func parseAgentChannelConfig(value any) AgentChannelConfig {
	node := mapNode(value)
	if len(node) == 0 {
		return AgentChannelConfig{}
	}
	cfg := AgentChannelConfig{
		ChannelID:      stringNode(node["channelId"]),
		RemoteAgentKey: stringNode(node["remoteAgentKey"]),
	}
	for _, item := range listMaps(node["exports"]) {
		export := AgentChannelExport{
			ChannelID:        stringNode(item["channelId"]),
			ExternalAgentKey: stringNode(item["externalAgentKey"]),
			Allow:            parseAgentChannelAllow(item["allow"]),
		}
		cfg.Exports = append(cfg.Exports, export)
	}
	return cfg
}

func parseAgentChannelAllow(value any) AgentChannelAllow {
	node := mapNode(value)
	allow := AgentChannelAllow{Query: true}
	if raw, ok := node["query"]; ok {
		allow.Query = boolNode(raw)
	}
	if raw, ok := node["submit"]; ok {
		allow.Submit = boolNode(raw)
	}
	if raw, ok := node["steer"]; ok {
		allow.Steer = boolNode(raw)
	}
	if raw, ok := node["interrupt"]; ok {
		allow.Interrupt = boolNode(raw)
	}
	if raw, ok := node["fileTransfer"]; ok {
		allow.FileTransfer = boolNode(raw)
	}
	return allow
}

func boolNode(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") ||
			strings.EqualFold(strings.TrimSpace(typed), "yes") ||
			strings.TrimSpace(typed) == "1"
	default:
		return false
	}
}

func firstAnyValue(values map[string]any, keys ...string) any {
	value, _ := firstExistingValue(values, keys...)
	return value
}

func firstExistingValue(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			return value, true
		}
	}
	return nil, false
}

func applyGlobalAgentFlags(def AgentDefinition, globalMemoryEnabled bool) AgentDefinition {
	if globalMemoryEnabled {
		return def
	}
	def.MemoryEnabled = false
	def.MemoryConfig.Enabled = false
	return def
}

func configureAgentKBaseCapability(def *AgentDefinition, raw map[string]any) error {
	if def == nil {
		return nil
	}
	isKBaseMode := strings.EqualFold(strings.TrimSpace(def.Mode), AgentModeKBase)
	_, enabledSet := raw["enabled"]
	if isKBaseMode {
		if enabledSet && !def.KBaseConfig.Enabled {
			return fmt.Errorf("kbaseConfig.enabled cannot be false for mode: KBASE")
		}
		def.KBaseConfig.Enabled = true
		def.KBaseRequirement = knowledge.RequirementRequired
	} else {
		def.KBaseRequirement = knowledge.RequirementOptional
		if len(raw) > 0 && !enabledSet {
			return fmt.Errorf("kbaseConfig.enabled must be explicitly configured for non-KBASE agents")
		}
		if def.KBaseConfig.Enabled {
			switch strings.ToUpper(strings.TrimSpace(def.Mode)) {
			case AgentModeGeneral, "PLAN_EXECUTE":
			case AgentModeCoder:
				if AgentUsesACPCoderBackend(*def) {
					return fmt.Errorf("kbaseConfig.enabled is not supported for ACP CODER agents")
				}
			default:
				return fmt.Errorf("kbaseConfig.enabled is only supported for GENERAL, PLAN-EXECUTE, native CODER, or KBASE agents")
			}
		}
	}
	return nil
}

func kbaseAgentHasFileTool(tools []string) bool {
	for _, name := range agentkbase.StructuredFileToolNames() {
		if containsString(tools, name) {
			return true
		}
	}
	return false
}

func validateReservedBashToolNames(tools []string) error {
	for _, tool := range tools {
		if tool == "platform_control" || tool == "desktop_action" {
			return fmt.Errorf("%s is retired; migrate to builtin.platform-control", tool)
		}
		if err := validateReservedBashToolName(tool, "toolConfig.tools"); err != nil {
			return err
		}
	}
	return nil
}

func validateReservedBashToolName(value string, field string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "_sandbox_bash_", "bash_sandbox":
		return fmt.Errorf("%s must use bash instead of %s", field, strings.TrimSpace(value))
	default:
		return nil
	}
}

func validateAgentSamplingConfig(path string, root map[string]any) error {
	modelConfig := mapNode(root["modelConfig"])
	if _, exists := modelConfig["sampling"]; exists {
		if err := contracts.ValidateSamplingSettings(modelConfig["sampling"], "modelConfig.sampling"); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	stageSettings := mapNode(root["stageSettings"])
	if len(stageSettings) == 0 {
		return nil
	}
	for _, stage := range []string{"plan", "planning", "execute", "summary"} {
		node := mapNode(stageSettings[stage])
		if len(node) == 0 {
			continue
		}
		modelConfig := mapNode(node["modelConfig"])
		if _, exists := modelConfig["sampling"]; exists {
			if err := contracts.ValidateSamplingSettings(modelConfig["sampling"], "stageSettings."+stage+".modelConfig.sampling"); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	return nil
}

func NormalizeAgentReasoningConfig(path string, root map[string]any) error {
	locations := []struct {
		name string
		node map[string]any
	}{
		{name: "modelConfig.reasoning", node: mapNode(mapNode(root["modelConfig"])["reasoning"])},
	}
	stageSettings := mapNode(root["stageSettings"])
	for _, stage := range []string{"plan", "planning", "execute", "summary"} {
		reasoning := mapNode(mapNode(mapNode(stageSettings[stage])["modelConfig"])["reasoning"])
		locations = append(locations, struct {
			name string
			node map[string]any
		}{name: "stageSettings." + stage + ".modelConfig.reasoning", node: reasoning})
	}
	for _, location := range locations {
		raw, exists := location.node["effort"]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s: %s.effort must be NONE, LOW, MEDIUM, HIGH, XHIGH, or MAX", path, location.name)
		}
		normalized, ok := models.NormalizeReasoningEffort(value)
		if !ok || normalized == "" {
			return fmt.Errorf("%s: %s.effort must be NONE, LOW, MEDIUM, HIGH, XHIGH, or MAX", path, location.name)
		}
		location.node["effort"] = normalized
		if normalized == models.ReasoningEffortNone {
			location.node["enabled"] = false
		}
	}
	return nil
}

// validatePlanningConfig rejects stage tool lists that planningMode no longer
// reads: planning and confirmed-plan Runs use the Agent's own tools, and their
// differences come only from planning-mode in agent-settings.yml.
func validatePlanningConfig(mode string, root map[string]any) error {
	stageSettings := mapNode(root["stageSettings"])
	stageTools := func(stage string) bool {
		_, exists := mapNode(mapNode(stageSettings[stage])["toolConfig"])["tools"]
		return exists
	}
	if agentbuiltin.PlanningModeSupported(mode) {
		// Neither list is read for a native Agent; accepting one would let a
		// config look narrowed while every Agent tool stays available.
		for _, stage := range []string{"planning", "execute"} {
			if stageTools(stage) {
				return fmt.Errorf("stageSettings.%s.toolConfig.tools is unsupported; configure planning-mode in agent-settings.yml", stage)
			}
		}
		// Every Run of a native Agent uses the Agent's own model: planning, the
		// Run started from a confirmed plan, and an ordinary Run. A second model
		// here made the persisted system-init disagree with the model requested.
		for _, stage := range []string{"planning", "execute"} {
			node := mapNode(stageSettings[stage])
			_, nested := mapNode(node["modelConfig"])["modelKey"]
			_, flat := node["modelKey"]
			if nested || flat {
				return fmt.Errorf("stageSettings.%s modelKey is unsupported; use modelConfig.modelKey", stage)
			}
		}
	}
	if !strings.EqualFold(strings.TrimSpace(mode), "CODER") {
		return nil
	}
	if _, exists := stageSettings["plan"]; exists {
		return fmt.Errorf("CODER stageSettings.plan is unsupported; use stageSettings.planning")
	}
	if _, exists := mapNode(mapNode(root["budget"])["stages"])["plan"]; exists {
		return fmt.Errorf("CODER budget.stages.plan is unsupported; use budget.stages.planning")
	}
	return nil
}

func normalizeWonderStrings(value any) []string {
	return normalizeAgentTextList(value)
}

func normalizeAgentTextList(value any) []string {
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

func parseRuntimeEnv(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("runtimeConfig.env must be a map[string]string")
	}
	if len(root) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(root))
	for key, rawValue := range root {
		if err := validateRuntimeEnvKey(key); err != nil {
			return nil, err
		}
		stringValue, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("runtimeConfig.env[%q] must be a string", key)
		}
		result[key] = stringValue
	}
	return result, nil
}

func validateRuntimeEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("runtimeConfig.env contains an empty key")
	}
	if agentconfig.IsReserved(key) {
		return fmt.Errorf("runtimeConfig.env key %q is reserved by Agent Platform", key)
	}
	if strings.ContainsRune(key, '=') {
		return fmt.Errorf("runtimeConfig.env key %q must not contain '='", key)
	}
	for _, r := range key {
		if unicode.IsSpace(r) {
			return fmt.Errorf("runtimeConfig.env key %q must not contain whitespace", key)
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
	for _, key := range []string{"plan", "planning", "execute", "summary"} {
		node := cloneMapForWrite(mapNode(stageSettings[key]))
		applyReasoningDefaultsToStageNode(node, enabled, enabledOK, effort, effortOK)
		stageSettings[key] = node
	}
	return stageSettings
}

func applyReasoningDefaultsToStageNode(node map[string]any, enabled any, enabledOK bool, effort any, effortOK bool) {
	modelConfig := cloneMapForWrite(mapNode(node["modelConfig"]))
	reasoning := cloneMapForWrite(mapNode(modelConfig["reasoning"]))
	if enabledOK {
		if _, exists := reasoning["enabled"]; !exists {
			reasoning["enabled"] = enabled
		}
	}
	if effortOK {
		if _, exists := reasoning["effort"]; !exists {
			reasoning["effort"] = effort
		}
	}
	if len(reasoning) > 0 {
		modelConfig["reasoning"] = reasoning
		node["modelConfig"] = modelConfig
	}
}

func applyModelSamplingDefaults(stageSettings map[string]any, modelSampling map[string]any) map[string]any {
	defaults := contracts.ParseSamplingSettings(modelSampling)
	if defaults.IsZero() {
		return stageSettings
	}
	if stageSettings == nil {
		stageSettings = map[string]any{}
	}
	for _, key := range []string{"plan", "planning", "execute", "summary"} {
		node := cloneMapForWrite(mapNode(stageSettings[key]))
		modelConfig := cloneMapForWrite(mapNode(node["modelConfig"]))
		merged := contracts.MergeSamplingSettings(defaults, contracts.ParseSamplingSettings(mapNode(modelConfig["sampling"])))
		if !merged.IsZero() {
			modelConfig["sampling"] = merged.ToMap()
			node["modelConfig"] = modelConfig
		}
		stageSettings[key] = node
	}
	return stageSettings
}

func cloneMapForWrite(values map[string]any) map[string]any {
	cloned := contracts.CloneMap(values)
	if cloned == nil {
		return map[string]any{}
	}
	return cloned
}
