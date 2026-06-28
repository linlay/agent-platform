package catalog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
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

func loadAgents(root, marketDir string, globalMemoryEnabled bool) (map[string]AgentDefinition, error) {
	items, _, err := loadAgentsWithAdmin(root, marketDir, globalMemoryEnabled)
	return items, err
}

func loadAgentsWithAdmin(root, marketDir string, globalMemoryEnabled bool) (map[string]AgentDefinition, map[string]AdminAgent, error) {
	items := map[string]AgentDefinition{}
	adminItems := map[string]AdminAgent{}
	err := visitRuntimeEntries(
		root,
		func(root string) {
			log.Printf("[catalog][agents] directory not found: %s", root)
		},
		func(name string, _ os.DirEntry) bool {
			return !strings.HasPrefix(name, ".") && ShouldLoadRuntimeName(name)
		},
		func(name string, entry os.DirEntry) {
			loadAgentSourceIntoMaps(root, name, entry, marketDir, globalMemoryEnabled, items, adminItems)
		},
	)
	if err != nil {
		return nil, nil, err
	}
	return items, adminItems, nil
}

func loadAgentSourceIntoMaps(root string, name string, entry os.DirEntry, marketDir string, globalMemoryEnabled bool, items map[string]AgentDefinition, adminItems map[string]AdminAgent) {
	source, ok := runtimeAgentSource(root, name, entry)
	if !ok {
		return
	}
	fallbackKey := adminAgentFallbackKey(source)
	definition, err := readAdminAgentDefinitionMap(source.Path)
	if err != nil {
		log.Printf("[catalog][agents] skip %s %s: parse error: %v", source.Kind, name, err)
		adminItems[fallbackKey] = invalidAdminAgent(source, fallbackKey, nil, "invalid_yaml", err)
		return
	}
	adminKey := adminAgentKey(source, fallbackKey, definition)
	def, _, err := parseAgentFileRaw(source.Path)
	if err != nil {
		log.Printf("[catalog][agents] skip %s %s: parse error: %v", source.Kind, name, err)
		adminItems[adminKey] = invalidAdminAgent(source, adminKey, definition, "invalid_config", err)
		return
	}
	if source.Kind == "directory" && def.Key != name {
		err := fmt.Errorf("key mismatch (file key=%q, directory=%q)", def.Key, name)
		log.Printf("[catalog][agents] skip directory %s: %v", name, err)
		adminItems[fallbackKey] = invalidAdminAgent(source, fallbackKey, definition, "key_mismatch", err)
		return
	}
	if source.Kind == "directory" {
		loadAgentPrompts(source.AgentDir, &def, definition)
		def.AgentDir = source.AgentDir
		if marketDir != "" && len(def.Skills) > 0 {
			if err := reconcileDeclaredSkills(source.AgentDir, def.Skills, marketDir); err != nil {
				log.Printf("[catalog][skills] sync %s: %v", def.Key, err)
			}
		}
	}
	def = applyGlobalAgentFlags(def, globalMemoryEnabled)
	items[def.Key] = def
	adminItems[def.Key] = readyAdminAgent(def, source, definition)
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
		Key:          def.Key,
		Name:         firstNonBlankString(def.Name, def.Key),
		Icon:         def.Icon,
		Description:  def.Description,
		Role:         def.Role,
		Mode:         apiMode,
		ModelKey:     def.ModelKey,
		Tools:        append([]string(nil), def.Tools...),
		Skills:       append([]string(nil), def.Skills...),
		Workspace:    def.Workspace,
		Controls:     cloneListMaps(def.Controls),
		Status:       AdminAgentStatusReady,
		Source:       source,
		Definition:   contracts.CloneMap(definition),
		SoulPrompt:   def.SoulPrompt,
		AgentsPrompt: def.AgentsPrompt,
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
	}
	soulPrompt := ""
	agentsPrompt := ""
	if source.Kind == "directory" && strings.TrimSpace(source.AgentDir) != "" {
		soulPrompt = readOptionalMarkdown(filepath.Join(source.AgentDir, "SOUL.md"))
		agentsPrompt = readOptionalMarkdown(filepath.Join(source.AgentDir, "AGENTS.md"))
	}
	item := AdminAgent{
		Key:          key,
		Name:         firstNonBlankString(stringNode(definition["name"]), key),
		Icon:         definition["icon"],
		Description:  stringNode(definition["description"]),
		Role:         stringNode(definition["role"]),
		Mode:         mode,
		ModelKey:     stringNode(modelConfig["modelKey"]),
		Tools:        listStrings(toolConfig["tools"]),
		Skills:       listStrings(mapNode(definition["skillConfig"])["skills"]),
		Workspace:    parseAgentWorkspaceRoot(runtimeConfig["workspaceRoot"]),
		Controls:     cloneListMaps(listMaps(definition["controls"])),
		Source:       source,
		Definition:   contracts.CloneMap(definition),
		SoulPrompt:   soulPrompt,
		AgentsPrompt: agentsPrompt,
	}
	item.Meta = adminAgentMeta(item, parseAgentVisibilityScopes(definition["visibility"]))
	return item
}

func adminAgentMeta(item AdminAgent, visibilityScopes []string) map[string]any {
	return map[string]any{
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
}

func loadAgentPrompts(agentDir string, def *AgentDefinition, root map[string]any) {
	if agentDir == "" {
		return
	}

	def.SoulPrompt = readOptionalMarkdown(filepath.Join(agentDir, "SOUL.md"))
	if !strings.EqualFold(def.Mode, AgentModeKBase) {
		def.StaticMemoryPrompt = readOptionalMarkdown(filepath.Join(agentDir, "memory", "memory.md"))
	}

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
	case "system", "session", "owner", "all-agents":
		return tag
	default:
		return ""
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
	for _, stage := range []string{"plan", "execute", "summary"} {
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
		Key:              stringNode(root["key"]),
		Name:             stringNode(root["name"]),
		Icon:             root["icon"],
		Description:      stringNode(root["description"]),
		Role:             stringNode(root["role"]),
		Greetings:        parseAgentGreetings(root),
		Wonders:          normalizeWonderStrings(root["wonders"]),
		Mode:             NormalizeAgentModeForRuntime(stringNode(root["mode"])),
		VisibilityScopes: parseAgentVisibilityScopes(root["visibility"]),
	}
	modelConfig := mapNode(root["modelConfig"])
	if err := validateAgentSamplingConfig(path, root); err != nil {
		return AgentDefinition{}, nil, err
	}
	def.ModelKey = stringNode(modelConfig["modelKey"])
	def.ServiceTier = stringNode(modelConfig["serviceTier"])
	toolConfig := mapNode(root["toolConfig"])
	def.Tools = listStrings(toolConfig["tools"])
	def.Skills = listStrings(mapNode(root["skillConfig"])["skills"])
	def.Controls = cloneListMaps(listMaps(root["controls"]))
	contextConfig := mapNode(root["contextConfig"])
	contextTags := listStrings(contextConfig["tags"])
	def.ContextTags = normalizeContextTags(contextTags)
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
			BaseURL:   stringNode(proxyRaw["baseUrl"]),
			Transport: normalizeProxyTransport(stringNode(proxyRaw["transport"])),
			AgentKey:  stringNode(proxyRaw["agentKey"]),
			ChatID:    stringNode(proxyRaw["chatId"]),
			Token:     resolveProxyToken(proxyRaw),
			TokenEnv:  stringNode(proxyRaw["tokenEnv"]),
			Timeout:   intNode(proxyRaw["timeout"]),
		}
		if def.ProxyConfig.Timeout <= 0 {
			def.ProxyConfig.Timeout = 300
		}
	}
	def.RuntimePrompts = parseRuntimePrompts(mapNode(root["runtimePrompts"]))
	runtimeConfig := mapNode(root["runtimeConfig"])
	if len(runtimeConfig) > 0 {
		def.ACPProxyID = stringNode(runtimeConfig["acpProxyId"])
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
	def.KBaseConfig = parseAgentKBaseConfig(root["kbaseConfig"])
	if err := validateAgentWorkspace(def.Workspace); err != nil {
		return AgentDefinition{}, nil, err
	}
	hasRuntimeSandbox := strings.TrimSpace(stringNode(def.Runtime["environmentId"])) != ""
	if err := validateAgentModeWorkspace(def.Mode, def.Workspace, hasRuntimeSandbox); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := ValidateAgentCoderBackend(def); err != nil {
		return AgentDefinition{}, nil, err
	}
	if err := ValidateAgentKBaseConfig(def); err != nil {
		return AgentDefinition{}, nil, err
	}
	def = applyAgentModeProfileDefaults(def)
	if strings.EqualFold(def.Mode, AgentModeKBase) {
		def = enforceKBaseAgentBoundaries(def)
	}

	if err := validateReservedBashToolNames(def.Tools); err != nil {
		return AgentDefinition{}, nil, err
	}
	if !strings.EqualFold(def.Mode, AgentModeKBase) && (len(def.Skills) > 0 || runtimeRequiresBash(def.Runtime)) && !containsString(def.Tools, "bash") {
		def.Tools = append(def.Tools, "bash")
	}
	if !strings.EqualFold(def.Mode, AgentModeKBase) {
		memoryConfig, err := parseAgentMemoryConfig(path, root["memoryConfig"])
		if err != nil {
			return AgentDefinition{}, nil, err
		}
		def.MemoryConfig = memoryConfig
		def.MemoryEnabled = def.MemoryConfig.Enabled
		if def.MemoryConfig.Enabled {
			for _, memTool := range []string{"memory_write", "memory_read", "memory_search"} {
				if !containsString(def.Tools, memTool) {
					def.Tools = append(def.Tools, memTool)
				}
			}
			if def.MemoryConfig.ManagementTools {
				for _, memTool := range []string{"memory_update", "memory_forget", "memory_timeline", "memory_promote", "memory_consolidate"} {
					if !containsString(def.Tools, memTool) {
						def.Tools = append(def.Tools, memTool)
					}
				}
			}
		}
	}

	if def.Key == "" {
		return AgentDefinition{}, nil, fmt.Errorf("agent key is required")
	}
	if def.Description == "" {
		def.Description = def.Key
	}
	if def.Role == "" && !strings.EqualFold(def.Mode, AgentModeCoder) && !strings.EqualFold(def.Mode, AgentModeKBase) {
		def.Role = def.Name
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
	case "@workspace", "@chat", "@agent", "@skills", "@skills-market", "@owner":
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
	if managementTools, ok := node["managementTools"].(bool); ok {
		cfg.ManagementTools = managementTools
	}
	embedding := mapNode(node["embedding"])
	cfg.Embedding = AgentMemoryEmbeddingConfig{
		ProviderKey: stringNode(embedding["providerKey"]),
		Model:       stringNode(embedding["model"]),
		Dimension:   intNode(embedding["dimension"]),
		Timeout:     intNode(embedding["timeout"]),
	}
	autoRemember := mapNode(node["autoRemember"])
	if enabled, ok := autoRemember["enabled"].(bool); ok {
		cfg.AutoRemember.Enabled = enabled
	}
	cfg.AutoRemember.ModelKey = stringNode(autoRemember["modelKey"])
	cfg.AutoRemember.Timeout = int64(intNode(autoRemember["timeout"]))
	return cfg, nil
}

func parseAgentKBaseConfig(value any) AgentKBaseConfig {
	node := mapNode(value)
	cfg := AgentKBaseConfig{
		Storage: AgentKBaseStorageConfig{Location: "runtime"},
		Include: []string{
			"**/*.md",
			"**/*.txt",
		},
		Exclude: []string{
			".git/**",
			".kbase/**",
			"node_modules/**",
		},
		Chunk: AgentKBaseChunkConfig{
			MaxChars:     4000,
			OverlapChars: 600,
		},
		Retrieval: AgentKBaseRetrievalConfig{
			TopK:         8,
			VectorWeight: 0.7,
			FTSWeight:    0.3,
		},
	}
	if len(node) == 0 {
		return cfg
	}
	embedding := mapNode(node["embedding"])
	cfg.Embedding = AgentKBaseEmbeddingConfig{
		ProviderKey: stringNode(embedding["providerKey"]),
		Model:       stringNode(embedding["model"]),
		Dimension:   intNode(embedding["dimension"]),
		Timeout:     intNode(embedding["timeout"]),
	}
	storage := mapNode(node["storage"])
	if location := strings.ToLower(strings.TrimSpace(stringNode(storage["location"]))); location != "" {
		cfg.Storage.Location = location
	}
	if include := listStrings(node["include"]); len(include) > 0 {
		cfg.Include = include
	}
	if exclude := listStrings(node["exclude"]); len(exclude) > 0 {
		cfg.Exclude = exclude
	}
	chunk := mapNode(node["chunk"])
	if maxChars := intNode(chunk["maxChars"]); maxChars > 0 {
		cfg.Chunk.MaxChars = maxChars
	}
	if maxChars := intNode(chunk["max-chars"]); maxChars > 0 {
		cfg.Chunk.MaxChars = maxChars
	}
	if _, exists := chunk["overlapChars"]; exists {
		if overlapChars := intNode(chunk["overlapChars"]); overlapChars >= 0 {
			cfg.Chunk.OverlapChars = overlapChars
		}
	}
	if _, exists := chunk["overlap-chars"]; exists {
		if overlapChars := intNode(chunk["overlap-chars"]); overlapChars >= 0 {
			cfg.Chunk.OverlapChars = overlapChars
		}
	}
	if cfg.Chunk.OverlapChars >= cfg.Chunk.MaxChars {
		cfg.Chunk.OverlapChars = cfg.Chunk.MaxChars / 5
	}
	retrieval := mapNode(node["retrieval"])
	if topK := intNode(retrieval["topK"]); topK > 0 {
		cfg.Retrieval.TopK = topK
	}
	if topK := intNode(retrieval["top-k"]); topK > 0 {
		cfg.Retrieval.TopK = topK
	}
	if weight := floatNode(retrieval["vectorWeight"]); weight > 0 {
		cfg.Retrieval.VectorWeight = weight
	}
	if weight := floatNode(retrieval["vector-weight"]); weight > 0 {
		cfg.Retrieval.VectorWeight = weight
	}
	if weight := floatNode(retrieval["ftsWeight"]); weight > 0 {
		cfg.Retrieval.FTSWeight = weight
	}
	if weight := floatNode(retrieval["fts-weight"]); weight > 0 {
		cfg.Retrieval.FTSWeight = weight
	}
	return cfg
}

func applyGlobalAgentFlags(def AgentDefinition, globalMemoryEnabled bool) AgentDefinition {
	if globalMemoryEnabled {
		return def
	}
	def.MemoryEnabled = false
	def.MemoryConfig.Enabled = false
	def.Tools = filterTools(def.Tools, func(tool string) bool {
		return !isMemoryTool(tool)
	})
	return def
}

func enforceKBaseAgentBoundaries(def AgentDefinition) AgentDefinition {
	def.StaticMemoryPrompt = ""
	def.MemoryEnabled = false
	def.MemoryConfig = AgentMemoryConfig{}
	def.Tools = filterTools(def.Tools, isKBaseTool)
	if len(def.Tools) == 0 {
		def.Tools = append([]string(nil), kbaseAgentProfile.Tools...)
	}
	return def
}

func filterTools(tools []string, keep func(string) bool) []string {
	if len(tools) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(tools))
	for _, tool := range tools {
		if keep(tool) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func isKBaseTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kbase_search", "kbase_read", "kbase_status", "kbase_refresh", "datetime":
		return true
	default:
		return false
	}
}

func isMemoryTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "memory_write", "memory_read", "memory_search",
		"memory_update", "memory_forget", "memory_timeline",
		"memory_promote", "memory_consolidate":
		return true
	default:
		return false
	}
}

func validateReservedBashToolNames(tools []string) error {
	for _, tool := range tools {
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

func runtimeRequiresBash(runtime map[string]any) bool {
	if len(runtime) == 0 {
		return false
	}
	if strings.TrimSpace(stringNode(runtime["environmentId"])) != "" {
		return true
	}
	env, ok := runtime["env"].(map[string]string)
	return ok && len(env) > 0
}

func parseAgentGreetings(root map[string]any) []string {
	if items := normalizeAgentTextList(root["greetings"]); len(items) > 0 {
		return items
	}
	return normalizeAgentTextList(root["greeting"])
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
	for _, stage := range []string{"plan", "execute", "summary"} {
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
	for _, key := range []string{"plan", "execute", "summary"} {
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
	for _, key := range []string{"plan", "execute", "summary"} {
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
