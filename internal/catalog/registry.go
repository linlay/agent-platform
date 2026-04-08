package catalog

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

type Registry interface {
	Agents(tag string) []api.AgentSummary
	Teams() []api.TeamSummary
	Skills(tag string) []api.SkillSummary
	Tools(kind string, tag string) []api.ToolSummary
	Tool(name string) (api.ToolDetailResponse, bool)
	DefaultAgentKey() string
	AgentDefinition(key string) (AgentDefinition, bool)
	TeamDefinition(teamID string) (TeamDefinition, bool)
	Reload(ctx context.Context, reason string) error
}

type AgentDefinition struct {
	Key           string
	Name          string
	Icon          any
	Description   string
	Role          string
	ModelKey      string
	Mode          string
	Tools         []string
	Skills        []string
	Controls      []map[string]any
	Sandbox       map[string]any
	ReactMaxSteps int
	ContextTags   []string
	Budget        map[string]any
	StageSettings map[string]any
	ToolOverrides map[string]api.ToolDetailResponse
}

type TeamDefinition struct {
	TeamID          string
	Name            string
	Icon            any
	AgentKeys       []string
	DefaultAgentKey string
}

type SkillDefinition struct {
	Key             string
	Name            string
	Description     string
	Prompt          string
	PromptTruncated bool
}

type FileRegistry struct {
	cfg   config.Config
	tools []api.ToolDetailResponse

	mu     sync.RWMutex
	agents map[string]AgentDefinition
	teams  map[string]TeamDefinition
	skills map[string]SkillDefinition
}

func NewFileRegistry(cfg config.Config, toolDefs []api.ToolDetailResponse) (*FileRegistry, error) {
	registry := &FileRegistry{
		cfg:    cfg,
		tools:  dedupeToolDefinitions(append(append([]api.ToolDetailResponse(nil), toolDefs...), confirmDialogTool())),
		agents: map[string]AgentDefinition{},
		teams:  map[string]TeamDefinition{},
		skills: map[string]SkillDefinition{},
	}
	if err := registry.Reload(context.Background(), "startup"); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *FileRegistry) Reload(_ context.Context, _ string) error {
	agents, err := loadAgents(r.cfg.Paths.AgentsDir)
	if err != nil {
		return err
	}
	teams, err := loadTeams(r.cfg.Paths.TeamsDir)
	if err != nil {
		return err
	}
	skills, err := loadSkills(r.cfg.Paths.SkillsMarketDir, r.cfg.Skills.MaxPromptChars)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.agents = agents
	r.teams = teams
	r.skills = skills
	r.mu.Unlock()
	return nil
}

func (r *FileRegistry) Agents(tag string) []api.AgentSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := sortedKeys(r.agents)
	items := make([]api.AgentSummary, 0, len(keys))
	needle := strings.ToLower(strings.TrimSpace(tag))
	for _, key := range keys {
		def := r.agents[key]
		summary := api.AgentSummary{
			Key:         def.Key,
			Name:        def.Name,
			Icon:        def.Icon,
			Description: def.Description,
			Role:        def.Role,
			Meta: map[string]any{
				"model":  def.ModelKey,
				"mode":   def.Mode,
				"tools":  append([]string(nil), def.Tools...),
				"skills": append([]string(nil), def.Skills...),
			},
		}
		if len(def.ContextTags) > 0 {
			summary.Meta["contextTags"] = append([]string(nil), def.ContextTags...)
		}
		if def.Budget != nil {
			summary.Meta["budget"] = cloneMap(def.Budget)
		}
		if def.StageSettings != nil {
			summary.Meta["stageSettings"] = cloneMap(def.StageSettings)
		}
		if def.Sandbox != nil {
			summary.Meta["sandbox"] = def.Sandbox
		}
		if needle != "" && !matchesAgentTag(summary, needle) {
			continue
		}
		items = append(items, summary)
	}
	return items
}

func (r *FileRegistry) Teams() []api.TeamSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agentKeys := sortedKeys(r.agents)
	agentsByID := make(map[string]AgentDefinition, len(agentKeys))
	for _, key := range agentKeys {
		agentsByID[key] = r.agents[key]
	}

	keys := sortedKeys(r.teams)
	items := make([]api.TeamSummary, 0, len(keys))
	for _, key := range keys {
		team := r.teams[key]
		invalidAgentKeys := make([]string, 0)
		icon := team.Icon
		for _, agentKey := range team.AgentKeys {
			agent, ok := agentsByID[agentKey]
			if !ok {
				invalidAgentKeys = append(invalidAgentKeys, agentKey)
				continue
			}
			if icon == nil {
				icon = agent.Icon
			}
		}
		defaultValid := team.DefaultAgentKey != "" && containsString(team.AgentKeys, team.DefaultAgentKey) && agentsByID[team.DefaultAgentKey].Key != ""
		if len(invalidAgentKeys) > 0 {
			log.Printf("[catalog][teams] team=%s invalidAgentKeys=%v", team.TeamID, invalidAgentKeys)
		}
		items = append(items, api.TeamSummary{
			TeamID:    team.TeamID,
			Name:      team.Name,
			Icon:      icon,
			AgentKeys: append([]string(nil), team.AgentKeys...),
			Meta: map[string]any{
				"invalidAgentKeys":     invalidAgentKeys,
				"defaultAgentKey":      team.DefaultAgentKey,
				"defaultAgentKeyValid": defaultValid,
			},
		})
	}
	return items
}

func (r *FileRegistry) Skills(tag string) []api.SkillSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(tag))
	keys := sortedKeys(r.skills)
	items := make([]api.SkillSummary, 0, len(keys))
	for _, key := range keys {
		skill := r.skills[key]
		if needle != "" && !matchesSkillTag(skill, needle) {
			continue
		}
		items = append(items, api.SkillSummary{
			Key:         skill.Key,
			Name:        skill.Name,
			Description: skill.Description,
			Meta: map[string]any{
				"promptTruncated": skill.PromptTruncated,
			},
		})
	}
	return items
}

func (r *FileRegistry) Tools(kind string, tag string) []api.ToolSummary {
	needleKind := strings.ToLower(strings.TrimSpace(kind))
	needleTag := strings.ToLower(strings.TrimSpace(tag))
	items := make([]api.ToolSummary, 0, len(r.tools))
	for _, tool := range r.tools {
		metaKind, _ := tool.Meta["kind"].(string)
		if needleKind != "" && strings.ToLower(metaKind) != needleKind {
			continue
		}
		if needleTag != "" && !matchesToolTag(tool, needleTag) {
			continue
		}
		items = append(items, api.ToolSummary{
			Key:         tool.Key,
			Name:        tool.Name,
			Label:       tool.Label,
			Description: tool.Description,
			Meta:        cloneMap(tool.Meta),
		})
	}
	return items
}

func (r *FileRegistry) Tool(name string) (api.ToolDetailResponse, bool) {
	needle := strings.TrimSpace(strings.ToLower(name))
	for _, tool := range r.tools {
		if strings.ToLower(tool.Name) == needle || strings.ToLower(tool.Key) == needle {
			return api.ToolDetailResponse{
				Key:           tool.Key,
				Name:          tool.Name,
				Label:         tool.Label,
				Description:   tool.Description,
				AfterCallHint: tool.AfterCallHint,
				Parameters:    cloneMap(tool.Parameters),
				Meta:          cloneMap(tool.Meta),
			}, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func (r *FileRegistry) DefaultAgentKey() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := sortedKeys(r.agents)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func (r *FileRegistry) AgentDefinition(key string) (AgentDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.agents[key]
	return def, ok
}

func (r *FileRegistry) TeamDefinition(teamID string) (TeamDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.teams[teamID]
	return def, ok
}

func resolveDirectoryAgentConfig(dirPath string) string {
	for _, candidate := range []string{"agent.yml", "agent.yaml"} {
		path := filepath.Join(dirPath, candidate)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func loadAgents(root string) (map[string]AgentDefinition, error) {
	items := map[string]AgentDefinition{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		log.Printf("[catalog][agents] directory not found: %s", root)
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !ShouldLoadRuntimeName(name) {
			continue
		}
		if entry.IsDir() {
			configPath := resolveDirectoryAgentConfig(filepath.Join(root, name))
			if configPath == "" {
				log.Printf("[catalog][agents] skip directory %s: no agent.yml or agent.yaml found", name)
				continue
			}
			def, err := parseAgentFile(configPath)
			if err != nil {
				log.Printf("[catalog][agents] skip directory %s: parse error: %v", name, err)
				continue
			}
			if def.Key != name {
				log.Printf("[catalog][agents] skip directory %s: key mismatch (file key=%q, directory=%q)", name, def.Key, name)
				continue
			}
			items[def.Key] = def
			continue
		}
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		def, err := parseAgentFile(filepath.Join(root, name))
		if err != nil {
			log.Printf("[catalog][agents] skip file %s: parse error: %v", name, err)
			continue
		}
		items[def.Key] = def
	}
	log.Printf("[catalog][agents] loaded %d agents from %s", len(items), root)
	return items, nil
}

func loadTeams(root string) (map[string]TeamDefinition, error) {
	items := map[string]TeamDefinition{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !ShouldLoadRuntimeName(name) || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
			continue
		}
		path := filepath.Join(root, name)
		def, err := parseTeamFile(path)
		if err != nil {
			log.Printf("[catalog][teams] skip file %s: parse error: %v", name, err)
			continue
		}
		items[def.TeamID] = def
	}
	log.Printf("[catalog][teams] loaded %d teams from %s", len(items), root)
	return items, nil
}

func loadSkills(root string, maxPromptChars int) (map[string]SkillDefinition, error) {
	items := map[string]SkillDefinition{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || !ShouldLoadRuntimeName(name) {
			continue
		}
		skillPath := filepath.Join(root, name, "SKILL.md")
		content, err := os.ReadFile(skillPath)
		if err != nil {
			log.Printf("[catalog][skills] skip directory %s: no SKILL.md found", name)
			continue
		}
		prompt := strings.TrimSpace(string(content))
		description := firstNonEmptyMarkdownLine(prompt)
		truncated := false
		if maxPromptChars > 0 && len(prompt) > maxPromptChars {
			truncated = true
		}
		items[name] = SkillDefinition{
			Key:             name,
			Name:            skillDisplayName(description, name),
			Description:     description,
			Prompt:          prompt,
			PromptTruncated: truncated,
		}
	}
	log.Printf("[catalog][skills] loaded %d skills from %s", len(items), root)
	return items, nil
}

func parseAgentFile(path string) (AgentDefinition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return AgentDefinition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return AgentDefinition{}, fmt.Errorf("agent file must be a map")
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
	def.Tools = append(def.Tools, listStrings(toolConfig["backends"])...)
	def.Tools = append(def.Tools, listStrings(toolConfig["frontends"])...)
	def.Tools = append(def.Tools, listStrings(toolConfig["actions"])...)
	def.ToolOverrides = parseToolOverrides(toolConfig["overrides"])
	def.Skills = listStrings(mapNode(root["skillConfig"])["skills"])
	def.Controls = cloneListMaps(listMaps(root["controls"]))
	def.ContextTags = listStrings(root["contextTags"])
	if budget := mapNode(root["budget"]); len(budget) > 0 {
		def.Budget = cloneMap(budget)
	}
	if stageSettings := mapNode(root["stageSettings"]); len(stageSettings) > 0 {
		def.StageSettings = cloneMap(stageSettings)
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

	// Implicit tool injection — mirrors Java AgentDefinitionLoader behaviour.
	// Agents with skillConfig.skills automatically get _sandbox_bash_.
	if len(def.Skills) > 0 && !containsString(def.Tools, "_sandbox_bash_") {
		def.Tools = append(def.Tools, "_sandbox_bash_")
	}
	// Agents with memoryConfig.enabled automatically get memory tools.
	memoryConfig := mapNode(root["memoryConfig"])
	if enabled, ok := memoryConfig["enabled"].(bool); ok && enabled {
		for _, memTool := range []string{"_memory_write_", "_memory_read_", "_memory_search_"} {
			if !containsString(def.Tools, memTool) {
				def.Tools = append(def.Tools, memTool)
			}
		}
	}

	if def.Key == "" {
		return AgentDefinition{}, fmt.Errorf("agent key is required")
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
	return def, nil
}

func parseTeamFile(path string) (TeamDefinition, error) {
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return TeamDefinition{}, err
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return TeamDefinition{}, fmt.Errorf("team file must be a map")
	}
	base := filepath.Base(path)
	teamID := strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
	return TeamDefinition{
		TeamID:          teamID,
		Name:            defaultString(stringNode(root["name"]), teamID),
		Icon:            root["icon"],
		AgentKeys:       listStrings(root["agentKeys"]),
		DefaultAgentKey: stringNode(root["defaultAgentKey"]),
	}, nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneListMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make([]map[string]any, 0, len(src))
	for _, item := range src {
		dst = append(dst, cloneMap(item))
	}
	return dst
}

func dedupeToolDefinitions(src []api.ToolDetailResponse) []api.ToolDetailResponse {
	if len(src) == 0 {
		return nil
	}
	out := make([]api.ToolDetailResponse, 0, len(src))
	seen := map[string]struct{}{}
	for _, tool := range src {
		dedupeKey := strings.ToLower(strings.TrimSpace(tool.Key)) + "|" + strings.ToLower(strings.TrimSpace(tool.Name))
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}
		out = append(out, tool)
	}
	return out
}

func matchesAgentTag(agent api.AgentSummary, needle string) bool {
	if strings.Contains(strings.ToLower(agent.Key), needle) || strings.Contains(strings.ToLower(agent.Name), needle) || strings.Contains(strings.ToLower(agent.Description), needle) || strings.Contains(strings.ToLower(agent.Role), needle) {
		return true
	}
	for _, key := range listStrings(agent.Meta["tools"]) {
		if strings.Contains(strings.ToLower(key), needle) {
			return true
		}
	}
	for _, key := range listStrings(agent.Meta["skills"]) {
		if strings.Contains(strings.ToLower(key), needle) {
			return true
		}
	}
	return false
}

func matchesSkillTag(skill SkillDefinition, needle string) bool {
	return strings.Contains(strings.ToLower(skill.Key), needle) ||
		strings.Contains(strings.ToLower(skill.Name), needle) ||
		strings.Contains(strings.ToLower(skill.Description), needle) ||
		strings.Contains(strings.ToLower(skill.Prompt), needle)
}

func matchesToolTag(tool api.ToolDetailResponse, needle string) bool {
	fields := []string{
		tool.Key,
		tool.Name,
		tool.Label,
		tool.Description,
		tool.AfterCallHint,
		stringNode(tool.Meta["kind"]),
		stringNode(tool.Meta["toolType"]),
		stringNode(tool.Meta["viewportKey"]),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func confirmDialogTool() api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:         "confirm_dialog",
		Name:        "confirm_dialog",
		Label:       "确认对话框",
		Description: "展示确认对话框并等待用户提交",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
			},
			"required": []string{"question"},
		},
		Meta: map[string]any{
			"kind":        "frontend",
			"toolType":    "html",
			"viewportKey": "confirm_dialog",
			"strict":      true,
			"sourceType":  "local",
			"sourceKey":   "confirm_dialog",
		},
	}
}

func stringNode(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return ""
	}
}

func intNode(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func mapNode(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func listStrings(value any) []string {
	switch v := value.(type) {
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if text := stringNode(item); text != "" {
				items = append(items, text)
			}
		}
		return items
	case []string:
		return append([]string(nil), v...)
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	default:
		return nil
	}
}

func listMaps(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			result = append(result, entry)
		}
	}
	return result
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func parseToolOverrides(value any) map[string]api.ToolDetailResponse {
	root := mapNode(value)
	if len(root) == 0 {
		return nil
	}
	result := make(map[string]api.ToolDetailResponse, len(root))
	for toolName, rawOverride := range root {
		override := mapNode(rawOverride)
		if len(override) == 0 {
			continue
		}
		name := defaultString(stringNode(override["name"]), toolName)
		key := defaultString(stringNode(override["key"]), toolName)
		meta := mapNode(override["meta"])
		if toolType := stringNode(override["toolType"]); toolType != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["toolType"] = toolType
		}
		if viewportKey := stringNode(override["viewportKey"]); viewportKey != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["viewportKey"] = viewportKey
		}
		result[strings.ToLower(strings.TrimSpace(toolName))] = api.ToolDetailResponse{
			Key:           key,
			Name:          name,
			Label:         stringNode(override["label"]),
			Description:   stringNode(override["description"]),
			AfterCallHint: stringNode(override["afterCallHint"]),
			Parameters:    cloneMap(firstMapNode(override["inputSchema"], override["parameters"])),
			Meta:          cloneMap(meta),
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func firstMapNode(values ...any) map[string]any {
	for _, value := range values {
		if mapped := mapNode(value); len(mapped) > 0 {
			return mapped
		}
	}
	return nil
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstNonEmptyMarkdownLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func skillDisplayName(description string, fallback string) string {
	if description != "" {
		return description
	}
	return fallback
}
