package catalog

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

type Registry interface {
	Agents(tag string) []api.AgentSummary
	Teams() []api.TeamSummary
	Skills(tag string) []api.SkillSummary
	SkillDefinition(key string) (SkillDefinition, bool)
	Tools(kind string, tag string) []api.ToolSummary
	Tool(name string) (api.ToolDetailResponse, bool)
	DefaultAgentKey() string
	AgentDefinition(key string) (AgentDefinition, bool)
	TeamDefinition(teamID string) (TeamDefinition, bool)
	Reload(ctx context.Context, reason string) error
}

type AgentDefinition struct {
	Key            string
	Name           string
	Icon           any
	Description    string
	Role           string
	ModelKey       string
	Mode           string
	Tools          []string
	Skills         []string
	Controls       []map[string]any
	Sandbox        map[string]any
	ReactMaxSteps  int
	ContextTags    []string
	Budget         map[string]any
	StageSettings  map[string]any
	ToolOverrides  map[string]api.ToolDetailResponse
	RuntimePrompts AgentRuntimePrompts
	AgentDir       string

	// PROXY mode: forward /api/query to a remote AGW-compatible service.
	ProxyConfig *ProxyConfig

	// Prompt files loaded from agent directory.
	SoulPrompt   string // from SOUL.md
	AgentsPrompt string // resolved from promptFile or AGENTS.md fallback

	// PLAN_EXECUTE stage prompts (stage-scoped promptFile).
	PlanPrompt    string
	ExecutePrompt string
	SummaryPrompt string
	MemoryPrompt  string
}

type AgentRuntimePrompts struct {
	Skill        SkillPromptConfig
	ToolAppendix ToolAppendixPromptConfig
	PlanExecute  PlanExecutePromptConfig
}

// ProxyConfig configures PROXY mode: forward /api/query to a remote
// AGW-compatible service (e.g. claude-code relay-server on port 3210).
type ProxyConfig struct {
	BaseURL   string // e.g. http://127.0.0.1:3210
	Token     string // optional Bearer token
	TimeoutMs int    // default 300000 (5 min)
}

type SkillPromptConfig struct {
	CatalogHeader     string
	DisclosureHeader  string
	InstructionsLabel string
}

type ToolAppendixPromptConfig struct {
	ToolDescriptionTitle string
	AfterCallHintTitle   string
}

type PlanExecutePromptConfig struct {
	TaskExecutionPromptTemplate string
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
	BashHooksDir    string
	SandboxEnv      map[string]string
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
		tools:  dedupeToolDefinitions(append([]api.ToolDetailResponse(nil), toolDefs...)),
		agents: map[string]AgentDefinition{},
		teams:  map[string]TeamDefinition{},
		skills: map[string]SkillDefinition{},
	}
	if err := registry.Reload(context.Background(), "startup"); err != nil {
		return nil, err
	}
	return registry, nil
}

// Reload reloads catalog entries scoped by reason. Supported reasons:
//
//	"startup" / "" / "config" — reload everything
//	"agents" — reload only agents
//	"teams"  — reload only teams
//	"skills" — reload only skills
//
// Other reasons fall through to a full reload.
func (r *FileRegistry) Reload(_ context.Context, reason string) error {
	switch reason {
	case "agents":
		agents, err := loadAgents(r.cfg.Paths.AgentsDir, r.cfg.Paths.SkillsMarketDir)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.agents = agents
		r.mu.Unlock()
		return nil
	case "teams":
		teams, err := loadTeams(r.cfg.Paths.TeamsDir)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.teams = teams
		r.mu.Unlock()
		return nil
	case "skills":
		skills, err := loadSkills(r.cfg.Paths.SkillsMarketDir, r.cfg.Skills.MaxPromptChars)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.skills = skills
		r.mu.Unlock()
		return nil
	}

	// Full reload (startup, config, or unknown reason)
	agents, err := loadAgents(r.cfg.Paths.AgentsDir, r.cfg.Paths.SkillsMarketDir)
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
			summary.Meta["budget"] = contracts.CloneMap(def.Budget)
		}
		if def.StageSettings != nil {
			summary.Meta["stageSettings"] = contracts.CloneMap(def.StageSettings)
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
			Meta:        contracts.CloneMap(tool.Meta),
		})
	}
	return items
}

func (r *FileRegistry) SkillDefinition(key string) (SkillDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.skills[key]
	return def, ok
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
				Parameters:    contracts.CloneMap(tool.Parameters),
				Meta:          contracts.CloneMap(tool.Meta),
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

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneListMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make([]map[string]any, 0, len(src))
	for _, item := range src {
		dst = append(dst, contracts.CloneMap(item))
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
		stringNode(tool.Meta["viewportType"]),
		stringNode(tool.Meta["viewportKey"]),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
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
		if viewportType := firstStringNode(override, "viewportType", "toolType"); viewportType != "" {
			if meta == nil {
				meta = map[string]any{}
			}
			meta["viewportType"] = viewportType
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
			Parameters:    contracts.CloneMap(firstMapNode(override["inputSchema"], override["parameters"])),
			Meta:          contracts.CloneMap(meta),
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func firstStringNode(root map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := stringNode(root[key]); text != "" {
			return text
		}
	}
	return ""
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
